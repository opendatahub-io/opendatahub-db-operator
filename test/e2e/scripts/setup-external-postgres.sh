#!/usr/bin/env bash
# Provision TLS and plaintext-only External PostgreSQL targets for e2e.
# TLS: external-postgres (verify-full + ca.crt).
# Plaintext: external-postgres-plaintext (prefer, no ca.crt) for prefer/allow fallback.
set -euo pipefail

cluster_name="${1:?usage: setup-external-postgres.sh KIND_CLUSTER}"
current_context="$(kubectl config current-context)"
if [[ "$current_context" != "kind-$cluster_name" ]]; then
  printf 'expected context kind-%s, got %s\n' "$cluster_name" "$current_context" >&2
  exit 1
fi

tls_namespace=external-postgres
tls_host=external-postgres.external-postgres.svc.cluster.local
plaintext_namespace=external-postgres-plaintext
plaintext_host=external-postgres.external-postgres-plaintext.svc.cluster.local
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -keyout "$workdir/server.key" -out "$workdir/server.crt" \
  -subj "/CN=$tls_host" \
  -addext "subjectAltName=DNS:$tls_host" \
  -addext 'basicConstraints=critical,CA:TRUE' >/dev/null 2>&1
tls_password="$(openssl rand -hex 24)"
plaintext_password="$(openssl rand -hex 24)"

kubectl create namespace "$tls_namespace"
kubectl create secret generic external-postgres-tls -n "$tls_namespace" \
  --from-file=tls.crt="$workdir/server.crt" \
  --from-file=tls.key="$workdir/server.key"
kubectl create secret generic external-postgres-admin -n "$tls_namespace" \
  --from-literal=host="$tls_host" \
  --from-literal=port=5432 \
  --from-literal=user=postgres \
  --from-literal=password="$tls_password" \
  --from-literal=dbname=postgres \
  --from-literal=sslmode=verify-full \
  --from-file=ca.crt="$workdir/server.crt"

kubectl apply -n "$tls_namespace" -f - <<'EOF'
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

kubectl create namespace "$plaintext_namespace"
kubectl create secret generic external-postgres-admin -n "$plaintext_namespace" \
  --from-literal=host="$plaintext_host" \
  --from-literal=port=5432 \
  --from-literal=user=postgres \
  --from-literal=password="$plaintext_password" \
  --from-literal=dbname=postgres \
  --from-literal=sslmode=prefer

kubectl apply -n "$plaintext_namespace" -f - <<'EOF'
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

kubectl rollout status deployment/external-postgres -n "$tls_namespace" --timeout=5m
kubectl rollout status deployment/external-postgres -n "$plaintext_namespace" --timeout=5m
