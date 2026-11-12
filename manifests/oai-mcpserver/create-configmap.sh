#!/bin/bash

kubectl create configmap oai-mcpserver --from-file=../../oai-mcpserver.yaml --dry-run=client -o yaml|kubectl apply -f  -
