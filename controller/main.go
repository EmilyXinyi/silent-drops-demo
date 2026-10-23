package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	flowpb "github.com/cilium/cilium/api/v1/flow"
	observerpb "github.com/cilium/cilium/api/v1/observer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var networkHealthGVR = schema.GroupVersionResource{
	Group: "demo.io", Version: "v1alpha1", Resource: "networkhealths",
}

type config struct {
	relayAddr         string
	namespace         string
	resourceName      string
	sourcePrefix      string
	destinationPrefix string
	healthyWindow     time.Duration
}

func main() {
	cfg := config{
		relayAddr:         env("HUBBLE_RELAY_ADDR", "hubble-relay.kube-system.svc:80"),
		namespace:         env("WATCH_NAMESPACE", "silent-drops"),
		resourceName:      env("NETWORKHEALTH_NAME", "frontend-backend"),
		sourcePrefix:      env("SOURCE_POD_PREFIX", "frontend-"),
		destinationPrefix: env("DESTINATION_POD_PREFIX", "backend-"),
		healthyWindow:     time.Duration(envInt("HEALTHY_WINDOW_SECONDS", 10)) * time.Second,
	}
	ctx := context.Background()

	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		// Useful for local development when running outside the cluster.
		kubeConfig, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
		if err != nil {
			log.Fatalf("load Kubernetes config: %v", err)
		}
	}
	kube, err := dynamic.NewForConfig(kubeConfig)
	if err != nil {
		log.Fatalf("create Kubernetes client: %v", err)
	}

	log.Printf("connecting to Hubble Relay at %s", cfg.relayAddr)
	// LOCAL-POC ONLY: use insecure transport only when the Relay endpoint is
	// explicitly configured for plaintext. For TLS-enabled Relay, replace this
	// with credentials.NewTLS(...) and mount the proper CA/client credentials.
	conn, err := grpc.DialContext(ctx, cfg.relayAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError(),
		grpc.WithTimeout(10*time.Second),
	)
	if err != nil {
		log.Fatalf("connect to Hubble Relay: %v (check service address and TLS mode)", err)
	}
	defer conn.Close()
	client := observerpb.NewObserverClient(conn)

	for {
		if err := watch(ctx, client, kube, cfg); err != nil {
			log.Printf("Hubble stream ended: %v; reconnecting in 2s", err)
			time.Sleep(2 * time.Second)
		}
	}
}

func watch(ctx context.Context, client observerpb.ObserverClient, kube dynamic.Interface, cfg config) error {
	req := &observerpb.GetFlowsRequest{
		Follow: true,
		Whitelist: []*flowpb.FlowFilter{
			{Verdict: []flowpb.Verdict{flowpb.Verdict_DROPPED}},
		},
	}
	stream, err := client.GetFlows(ctx, req)
	if err != nil {
		return fmt.Errorf("GetFlows: %w", err)
	}

	var lastDrop time.Time
	var dropCount int64
	healthy := true
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	// Receive flows in a goroutine so the main loop can also evaluate recovery.
	type flowResult struct {
		flow *flowpb.Flow
		err  error
	}
	flowCh := make(chan flowResult)
	go func() {
		for {
			resp, err := stream.Recv()
			if err != nil {
				select {
				case flowCh <- flowResult{err: err}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case flowCh <- flowResult{flow: resp.GetFlow()}:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !healthy && !lastDrop.IsZero() && time.Since(lastDrop) >= cfg.healthyWindow {
				healthy = true
				msg := "No relevant dropped flows observed during the recovery window"
				if err := updateStatus(ctx, kube, cfg, true, "NoRecentDrops", msg, dropCount, &lastDrop); err != nil {
					log.Printf("update healthy status: %v", err)
				} else {
					log.Printf("NetworkHealth Healthy after recovery window")
				}
			}
		case result := <-flowCh:
			if result.err != nil {
				return fmt.Errorf("receive flow: %w", result.err)
			}
			f := result.flow
			if f == nil || !isRelevantDrop(f, cfg) {
				continue
			}
			now := time.Now().UTC()
			lastDrop = now
			dropCount++
			healthy = false
			msg := fmt.Sprintf("Hubble observed a dropped flow from %s to %s", endpointName(f.GetSource(), f.GetIP().GetSource()), endpointName(f.GetDestination(), f.GetIP().GetDestination()))
			if err := updateStatus(ctx, kube, cfg, false, "DroppedFlowObserved", msg, dropCount, &lastDrop); err != nil {
				log.Printf("update degraded status: %v", err)
			} else {
				log.Printf("NetworkHealth Degraded: %s", msg)
			}
		}
	}
}

func isRelevantDrop(f *flowpb.Flow, cfg config) bool {
	if f.GetVerdict() != flowpb.Verdict_DROPPED {
		return false
	}
	switch f.GetDropReasonDesc() {
	case flowpb.DropReason_POLICY_DENIED, flowpb.DropReason_POLICY_DENY:
	default:
		return false
	}
	dst := f.GetDestination()
	if dst.GetNamespace() != cfg.namespace || !strings.HasPrefix(dst.GetPodName(), cfg.destinationPrefix) {
		return false
	}
	// When Service traffic is translated and masqueraded in the host stack
	// (e.g. k3s kube-proxy alongside Cilium), the drop at the destination
	// shows the node's cilium_host IP as its source, with no pod metadata.
	// Only enforce the source match when Hubble attributed the flow to a pod.
	src := f.GetSource()
	if src.GetPodName() == "" {
		return true
	}
	return src.GetNamespace() == cfg.namespace && strings.HasPrefix(src.GetPodName(), cfg.sourcePrefix)
}

func endpointName(ep *flowpb.Endpoint, ip string) string {
	if name := ep.GetPodName(); name != "" {
		return name
	}
	if ip != "" {
		return ip
	}
	return "unknown"
}

func updateStatus(ctx context.Context, kube dynamic.Interface, cfg config, healthy bool, reason, message string, count int64, lastDrop *time.Time) error {
	lastObserved := time.Now().UTC().Format(time.RFC3339)
	status := map[string]any{
		"healthy":      healthy,
		"reason":       reason,
		"message":      message,
		"droppedFlows": count,
		"lastObserved": lastObserved,
	}
	if lastDrop != nil {
		status["lastDrop"] = lastDrop.Format(time.RFC3339)
	}
	patch := map[string]any{"status": status}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = kube.Resource(networkHealthGVR).Namespace(cfg.namespace).Patch(
		ctx, cfg.resourceName, typesMergePatchType(), body, metav1.PatchOptions{}, "status",
	)
	return err
}

func typesMergePatchType() types.PatchType {
	return types.MergePatchType
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}
