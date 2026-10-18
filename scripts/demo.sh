#!/usr/bin/env bash
set -euo pipefail
echo "1) Apply the blocking policy"
kubectl apply -f demo/network-policy-block.yaml
echo
echo "2) In another terminal, generate traffic:"
echo "   while true; do curl -sS -i http://localhost:8080/; sleep 1; done"
echo
echo "3) Observe relevant drops:"
echo "   hubble observe --verdict DROPPED --namespace silent-drops"
echo
echo "4) Watch health:"
echo "   kubectl -n silent-drops get networkhealth frontend-backend -o yaml -w"
echo
echo "5) Restore the allow policy:"
echo "   kubectl apply -f demo/network-policy-allow.yaml"
