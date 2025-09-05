package utils

import (
	"crypto/x509"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func SetupGrpcCredential(tls bool, hostName string, customCA ...string) (credentials.TransportCredentials, error) {
	var credential credentials.TransportCredentials
	if tls {
		pool, err := x509.SystemCertPool()
		if err != nil {
			if len(customCA) == 0 {
				return nil, fmt.Errorf("addtional ca not found while system cert pool can not be copied: %w", err)
			}
			log.Println("copy system cert pool failed, creating new empty pool")
			pool = x509.NewCertPool()
		}

		// load custom cas if exist
		if err = loadCertificateAuthorities(pool, customCA); err != nil {
			return nil, fmt.Errorf("load custom ca failed: %w", err)
		}

		// error handling omitted
		credential = credentials.NewClientTLSFromCert(pool, hostName)
	} else {
		credential = insecure.NewCredentials()
	}

	return credential, nil
}

func loadCertificateAuthorities(pool *x509.CertPool, customCAList []string) error {
	for _, path := range customCAList {
		// Clean the path to remove any directory traversal attempts
		cleanPath := filepath.Clean(path)

		// Read the CA file
		cert, err := os.ReadFile(cleanPath)
		if err != nil {
			return err
		}

		if !pool.AppendCertsFromPEM(cert) {
			return fmt.Errorf("failed to append %s to CA certificates", cleanPath)
		}
	}
	return nil
}
