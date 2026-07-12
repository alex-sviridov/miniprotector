package jobid

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

func TestResolve_ReturnsGivenIDWhenNonEmpty(t *testing.T) {
	assert.Equal(t, "explicit-id", Resolve("explicit-id"))
}

func TestResolve_GeneratesDistinctUUIDsWhenEmpty(t *testing.T) {
	first := Resolve("")
	second := Resolve("")
	assert.NotEmpty(t, first)
	assert.NotEmpty(t, second)
	assert.NotEqual(t, first, second)
}

func TestOutgoing_AttachesJobIDMetadata(t *testing.T) {
	ctx := Outgoing(context.Background(), "my-job")
	md, ok := metadata.FromOutgoingContext(ctx)
	require.True(t, ok)
	assert.Equal(t, []string{"my-job"}, md.Get("job-id"))
}

func TestFromIncoming_ReturnsAttachedValue(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("job-id", "my-job"))
	id, err := FromIncoming(ctx)
	require.NoError(t, err)
	assert.Equal(t, "my-job", id)
}

func TestFromIncoming_NoMetadataReturnsError(t *testing.T) {
	_, err := FromIncoming(context.Background())
	assert.Error(t, err)
}

func TestFromIncoming_EmptyJobIDReturnsError(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("job-id", ""))
	_, err := FromIncoming(ctx)
	assert.Error(t, err)
}

func TestFromIncoming_MissingJobIDKeyReturnsError(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("other-key", "value"))
	_, err := FromIncoming(ctx)
	assert.Error(t, err)
}
