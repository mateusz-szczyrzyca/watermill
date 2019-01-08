package http

import (
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/stretchr/testify/require"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/infrastructure"
)

func createPubSub(t *testing.T) message.PubSub {
	logger := watermill.NewStdLogger(true, true)

	// use any free port to allow parallel tests
	sub, err := NewSubscriber(":0", SubscriberConfig{}, logger)
	require.NoError(t, err)

	_, err = sub.StartHTTPServer()
	require.NoError(t, err)

	publisherConf := PublisherConfig{
		MarshalMessageFunc: DefaultMarshalMessageFunc("http://" + sub.Addr().String()),
	}

	pub, err := NewPublisher(publisherConf, logger)
	require.NoError(t, err)

	retryConf := RetryPublisherConfig{
		MaxRetries:       10,
		TimeToFirstRetry: time.Millisecond,
	}

	// use the retry decorator, for tests involving retry after error
	retryPub, err := NewRetryPublisher(pub, retryConf)
	require.NoError(t, err)

	return message.NewPubSub(retryPub, sub)
}

func TestPublishSubscribe(t *testing.T) {
	infrastructure.TestPubSub(
		t,
		infrastructure.Features{
			ConsumerGroups:      false,
			ExactlyOnceDelivery: true,
			GuaranteedOrder:     true,
			Persistent:          false,
		},
		createPubSub,
		nil,
	)
}
