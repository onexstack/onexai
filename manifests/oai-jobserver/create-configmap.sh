#!/bin/bash

kubectl create configmap oai-jobserver --from-file=../../oai-jobserver.yaml --dry-run=client -o yaml|kubectl apply -f  -
