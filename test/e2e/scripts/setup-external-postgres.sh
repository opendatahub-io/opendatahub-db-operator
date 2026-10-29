#!/usr/bin/env bash
set -euo pipefail

cluster_name="${1:?usage: setup-external-postgres.sh KIND_CLUSTER}"
current_context="$(kubectl config current-context)"
if [[ "$current_context" != "kind-$cluster_name" ]]; then
  printf 'expected context kind-%s, got %s\n' "$cluster_name" "$current_context" >&2
  exit 1
fi

namespace=external-postgres
host=external-postgres.external-postgres.svc.cluster.local
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$workdir/server.key" -out "$workdir/server.crt" \
  -subj "/CN=$host" \
  -addext "subjectAltName=DNS:$host" \
  -addext 'basicConstraints=critical,CA:TRUE' >/dev/null 2>&1
password="$(openssl rand -hex 24)"

kubectl create namespace "$namespace"
kubectl create secret generic external-postgres-tls -n "$namespace" \
  --from-file=tls.crt="$workdir/server.crt" \
  --from-file=tls.key="$workdir/server.key"
kubectl create secret generic external-postgres-admin -n "$namespace" \
  --from-literal=host="$host" \
  --from-literal=port=5432 \
  --from-literal=user=postgres \
  --from-literal=password="$password" \
  --from-literal=dbname=postgres \
  --from-literal=sslmode=verify-full \
  --from-file=ca.crt="$workdir/server.crt"

kubectl apply -n "$namespace" -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: external-postgres
spec:
  replicas: 1
  selector:
    matchLabels:
      app: external-postgres
  template:
    metadata:
      labels:
        app: external-postgres
    spec:
      containers:
        - name: postgres
          image: postgres:16
          command:
            - /bin/bash
            - -ec
            - |
              cp /tls-source/tls.crt /var/lib/postgresql/server.crt
              cp /tls-source/tls.key /var/lib/postgresql/server.key
              chown postgres:postgres /var/lib/postgresql/server.crt /var/lib/postgresql/server.key
              chmod 600 /var/lib/postgresql/server.key
              exec docker-entrypoint.sh postgres \
                -c ssl=on \
                -c ssl_cert_file=/var/lib/postgresql/server.crt \
                -c ssl_key_file=/var/lib/postgresql/server.key
          env:
            - name: POSTGRES_USER
              value: postgres
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: external-postgres-admin
                  key: password
          ports:
            - containerPort: 5432
              name: postgres
          readinessProbe:
            exec:
              command: [/bin/sh, -ec, 'pg_isready -U postgres']
            periodSeconds: 2
          volumeMounts:
            - name: tls-source
              mountPath: /tls-source
              readOnly: true
      volumes:
        - name: tls-source
          secret:
            secretName: external-postgres-tls
---
apiVersion: v1
kind: Service
metadata:
  name: external-postgres
spec:
  selector:
    app: external-postgres
  ports:
    - port: 5432
      targetPort: postgres
EOF

kubectl rollout status deployment/external-postgres -n "$namespace" --timeout=5m
