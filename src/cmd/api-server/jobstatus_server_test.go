package main

import (
	"context"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestGetPolicyJobStatus_FinishedReturnsTrueWithTimestamp(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|policy-server"} | job_id="restore:x:1" | event="finish"`: {
			{Stream: map[string]string{"hostname": "web-01"}, Values: []lokiValue{
				{Timestamp: 1_700_000_000_000_000_000, Metadata: map[string]string{"job_id": "restore:x:1", "status": "success"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake

	createdAt := time.Unix(1_699_999_000, 0)
	resp, err := srv.GetPolicyJobStatus(context.Background(), &pb.GetPolicyJobStatusRequest{
		JobId:           "restore:x:1",
		PolicyCreatedAt: timestamppb.New(createdAt),
	})

	require.NoError(t, err)
	assert.True(t, resp.GetFinished())
	assert.Equal(t, int64(1_700_000_000), resp.GetFinishedAt().AsTime().Unix())
}

func TestGetPolicyJobStatus_NotFoundReturnsFalse(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake

	resp, err := srv.GetPolicyJobStatus(context.Background(), &pb.GetPolicyJobStatusRequest{
		JobId:           "restore:x:1",
		PolicyCreatedAt: timestamppb.New(time.Now()),
	})

	require.NoError(t, err)
	assert.False(t, resp.GetFinished())
}

func TestGetPolicyJobStatus_ClampsSinceToMaxJobsWindow(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake

	oldCreatedAt := time.Now().Add(-30 * 24 * time.Hour) // far older than maxJobsWindow (168h)
	_, err := srv.GetPolicyJobStatus(context.Background(), &pb.GetPolicyJobStatusRequest{
		JobId:           "restore:x:1",
		PolicyCreatedAt: timestamppb.New(oldCreatedAt),
	})
	require.NoError(t, err)

	assert.WithinDuration(t, time.Now().Add(-maxJobsWindow), fake.lastStart, 5*time.Second,
		"since must be clamped to maxJobsWindow before now, not the far-older policy_created_at")
}

func TestGetPolicyJobStatus_MissingJobIDRejected(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}

	_, err := srv.GetPolicyJobStatus(context.Background(), &pb.GetPolicyJobStatusRequest{
		PolicyCreatedAt: timestamppb.New(time.Now()),
	})
	assert.Error(t, err)
}
