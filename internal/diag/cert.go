package diag

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CertInfo struct {
	Name       string
	Namespace  string
	SecretName string
	CommonName string
	Expiry     time.Time
	DaysLeft   int
}

func (e *Engine) CertCheck(filterNS string, warnDays int) ([]CertInfo, error) {
	ns := filterNS
	if ns == "" {
		ns = e.ns()
	}
	var certs []CertInfo
	secrets, err := e.k8s.CoreV1().Secrets(ns).List(e.ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing secrets: %w", err)
	}
	for _, secret := range secrets.Items {
		if secret.Type != "kubernetes.io/tls" {
			continue
		}
		certData, ok := secret.Data["tls.crt"]
		if !ok {
			continue
		}
		info := parseCert(certData)
		if info == nil {
			continue
		}
		daysLeft := int(time.Until(info.NotAfter).Hours() / 24)
		if daysLeft > warnDays {
			continue
		}
		certs = append(certs, CertInfo{
			Name: secret.Name, Namespace: secret.Namespace, SecretName: secret.Name,
			CommonName: info.Subject.CommonName, Expiry: info.NotAfter, DaysLeft: daysLeft,
		})
	}
	ingresses, err := e.k8s.NetworkingV1().Ingresses(ns).List(e.ctx, metav1.ListOptions{})
	if err == nil {
		for _, ing := range ingresses.Items {
			for _, tls := range ing.Spec.TLS {
				if tls.SecretName == "" {
					continue
				}
				already := false
				for _, c := range certs {
					if c.SecretName == tls.SecretName && c.Namespace == ing.Namespace {
						already = true
						break
					}
				}
				if already {
					continue
				}
				secret, err := e.k8s.CoreV1().Secrets(ing.Namespace).Get(e.ctx, tls.SecretName, metav1.GetOptions{})
				if err != nil {
					continue
				}
				certData, ok := secret.Data["tls.crt"]
				if !ok {
					continue
				}
				info := parseCert(certData)
				if info == nil {
					continue
				}
				daysLeft := int(time.Until(info.NotAfter).Hours() / 24)
				if daysLeft > warnDays {
					continue
				}
				certs = append(certs, CertInfo{
					Name:       fmt.Sprintf("%s (ingress: %s)", tls.SecretName, ing.Name),
					Namespace:  ing.Namespace, SecretName: tls.SecretName,
					CommonName: info.Subject.CommonName, Expiry: info.NotAfter, DaysLeft: daysLeft,
				})
			}
		}
	}
	sort.Slice(certs, func(i, j int) bool { return certs[i].DaysLeft < certs[j].DaysLeft })
	return certs, nil
}

func parseCert(data []byte) *x509.Certificate {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	return cert
}
