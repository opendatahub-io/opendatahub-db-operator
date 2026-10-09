package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/opendatahub-io/odh-platform-utilities/framework/cluster"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	odhtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/resources/gvk"
)

func CertManagerAvailable(ctx context.Context, cli client.Client) (bool, error) {
	issuer, err := cluster.HasCRD(ctx, cli, gvk.CertManagerIssuer)
	if err != nil || !issuer {
		return false, err
	}
	certificate, err := cluster.HasCRD(ctx, cli, gvk.CertManagerCertificate)
	return certificate, err
}

func MarkCertManagerMissing(rr *odhtypes.ReconciliationRequest) {
	rr.Conditions.Mark(ConditionTLSConfiguration, metav1.ConditionFalse,
		conditions.WithReason("CertManagerUnavailable"),
		conditions.WithMessage("TLS requires cert-manager, but its CRDs are not installed"))
}
