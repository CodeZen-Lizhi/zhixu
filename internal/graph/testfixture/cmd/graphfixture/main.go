//go:build integration

// graphfixture seeds and removes committed Graph integration fixtures.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/graph/testfixture"
	"github.com/jackc/pgx/v5/pgxpool"
)

const commandTimeout = 60 * time.Second

type seedOutput struct {
	WorkspaceID          string `json:"workspace_id"`
	PrimaryTopicID       string `json:"primary_topic_id"`
	SecondaryTopicID     string `json:"secondary_topic_id"`
	FirstClaimID         string `json:"first_claim_id"`
	SecondClaimID        string `json:"second_claim_id"`
	MembershipRelationID string `json:"membership_relation_id"`
	SupportRelationID    string `json:"support_relation_id"`
}

type semanticLinkSeedOutput struct {
	seedOutput
	DiscoveryClaimID string `json:"discovery_claim_id"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, writer io.Writer) error {
	if writer == nil {
		return errors.New("graph fixture output is required")
	}
	command, workspaceID, err := parseArgs(args)
	if err != nil {
		return err
	}
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" || strings.TrimSpace(databaseURL) != databaseURL {
		return errors.New("ZHIXU_TEST_DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return errors.New("graph fixture database connection failed")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return errors.New("graph fixture database connection failed")
	}

	switch command {
	case "seed":
		fixture, seedErr := testfixture.SeedFunctional(ctx, pool)
		if seedErr != nil {
			return errors.New("graph fixture seed failed")
		}
		output := seedOutput{
			WorkspaceID:          string(fixture.WorkspaceID),
			PrimaryTopicID:       string(fixture.PrimaryTopicID),
			SecondaryTopicID:     string(fixture.SecondaryTopicID),
			FirstClaimID:         string(fixture.FirstClaimID),
			SecondClaimID:        string(fixture.SecondClaimID),
			MembershipRelationID: string(fixture.MembershipRelationID),
			SupportRelationID:    string(fixture.SupportRelationID),
		}
		if err := json.NewEncoder(writer).Encode(output); err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cleanupCancel()
			if cleanupErr := testfixture.Cleanup(cleanupCtx, pool, fixture.WorkspaceID); cleanupErr != nil {
				return errors.Join(errors.New("graph fixture output failed"), errors.New("graph fixture cleanup after output failure failed"))
			}
			return errors.New("graph fixture output failed")
		}
		return nil
	case "seed-semantic-link":
		fixture, seedErr := testfixture.SeedSemanticLinkBrowser(ctx, pool)
		if seedErr != nil {
			return errors.New("semantic-link browser fixture seed failed")
		}
		output := semanticLinkSeedOutput{
			seedOutput: seedOutput{
				WorkspaceID:          string(fixture.WorkspaceID),
				PrimaryTopicID:       string(fixture.PrimaryTopicID),
				SecondaryTopicID:     string(fixture.SecondaryTopicID),
				FirstClaimID:         string(fixture.FirstClaimID),
				SecondClaimID:        string(fixture.SecondClaimID),
				MembershipRelationID: string(fixture.MembershipRelationID),
				SupportRelationID:    string(fixture.SupportRelationID),
			},
			DiscoveryClaimID: string(fixture.DiscoveryClaimID),
		}
		if err := json.NewEncoder(writer).Encode(output); err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cleanupCancel()
			if cleanupErr := testfixture.CleanupSemanticLinkBrowser(cleanupCtx, pool, fixture.WorkspaceID); cleanupErr != nil {
				return errors.Join(errors.New("semantic-link browser fixture output failed"), errors.New("semantic-link browser fixture cleanup after output failure failed"))
			}
			return errors.New("semantic-link browser fixture output failed")
		}
		return nil
	case "cleanup-semantic-link":
		if err := testfixture.CleanupSemanticLinkBrowser(ctx, pool, workspaceID); err != nil {
			return errors.New("semantic-link browser fixture cleanup failed")
		}
		return nil
	case "cleanup":
		if err := testfixture.Cleanup(ctx, pool, workspaceID); err != nil {
			return errors.New("graph fixture cleanup failed")
		}
		return nil
	default:
		return errors.New("graph fixture command is invalid")
	}
}

func parseArgs(args []string) (string, foundation.ID, error) {
	if len(args) == 1 && args[0] == "seed" {
		return "seed", "", nil
	}
	if len(args) == 1 && args[0] == "seed-semantic-link" {
		return "seed-semantic-link", "", nil
	}
	if len(args) == 3 && args[0] == "cleanup" && args[1] == "--workspace-id" {
		workspaceID, err := foundation.ParseID(args[2])
		if err != nil || string(workspaceID) != args[2] {
			return "", "", errors.New("cleanup requires a canonical --workspace-id")
		}
		return "cleanup", workspaceID, nil
	}
	if len(args) == 3 && args[0] == "cleanup-semantic-link" && args[1] == "--workspace-id" {
		workspaceID, err := foundation.ParseID(args[2])
		if err != nil || string(workspaceID) != args[2] {
			return "", "", errors.New("cleanup-semantic-link requires a canonical --workspace-id")
		}
		return "cleanup-semantic-link", workspaceID, nil
	}
	return "", "", errors.New("usage: graphfixture seed | seed-semantic-link | cleanup --workspace-id <uuid> | cleanup-semantic-link --workspace-id <uuid>")
}
