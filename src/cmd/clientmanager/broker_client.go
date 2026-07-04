package main

import (
	"context"
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
)

// minter mints an enrollment token for hostname via a broker. Tests inject
// a stub; production wires mintToken.
type minter func(conf *config.Config, certsDir, hostname string, sans []string) (string, error)

// mintToken dials certrequest serve (at conf.CertrequestHost:CertrequestPort)
// over mTLS using this node's own identity and asks it to mint an
// enrollment token for hostname. client-manager never holds the CA's
// provisioner password itself -- see docs/components/certrequest.md's
// "serve mode" section for why.
func mintToken(conf *config.Config, certsDir, hostname string, sans []string) (string, error) {
	if conf.CertrequestHost == "" {
		return "", fmt.Errorf("certrequest_host not set in local.conf")
	}
	conn, err := connection.Connect(conf.CertrequestHost, conf.CertrequestPort, 5, certsDir)
	if err != nil {
		return "", fmt.Errorf("connect to certrequest serve: %w", err)
	}
	defer conn.Close()

	client := pb.NewEnrollmentBrokerServiceClient(conn)
	resp, err := client.MintEnrollmentToken(context.Background(), &pb.MintEnrollmentTokenRequest{
		Hostname: hostname,
		Sans:     sans,
	})
	if err != nil {
		return "", fmt.Errorf("mint enrollment token: %w", err)
	}
	return resp.Token, nil
}
