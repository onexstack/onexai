#!/bin/bash

kubectl create configmap oai-mqserver --from-file=../../oai-mqserver.yaml --dry-run=client -o yaml|kubectl apply -f  -
