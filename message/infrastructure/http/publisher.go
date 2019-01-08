package http

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"

	"github.com/pkg/errors"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
)

var (
	// ErrPublisherClosed happens when trying to publish to a topic while the publisher is closed or closing.
	ErrPublisherClosed = errors.New("publisher is closed")
	ErrNoMarshalFunc   = errors.New("marshal function is missing")

	ErrErrorResponse = errors.New("server responded with error status")
)

type MarshalMessageFunc func(topic string, msg *message.Message) (*http.Request, error)

// DefaultMarshalMessageFunc returns a MarshalMessage func transforming the message into a HTTP POST request.
// It encodes the UUID and Metadata in request headers.
// The request URL is combined from the base address and the topic.
func DefaultMarshalMessageFunc(address string) MarshalMessageFunc {
	return func(topic string, msg *message.Message) (*http.Request, error) {
		req, err := http.NewRequest(http.MethodPost, address+"/"+topic, bytes.NewBuffer(msg.Payload))
		if err != nil {
			return nil, err
		}

		req.Header.Set(HeaderUUID, msg.UUID)

		metadataJson, err := json.Marshal(msg.Metadata)
		if err != nil {
			return nil, errors.Wrap(err, "could not marshal metadata to JSON")
		}
		req.Header.Set(HeaderMetadata, string(metadataJson))
		return req, nil
	}
}

type Publisher struct {
	logger watermill.LoggerAdapter
	config PublisherConfig

	closed bool
}

type PublisherConfig struct {
	MarshalMessageFunc MarshalMessageFunc
	Client             *http.Client
	// if false (default), when server responds with error (>=400) to the webhook request, the response body is logged.
	DoNotLogResponseBodyOnServerError bool
}

func (c *PublisherConfig) setDefaults() {
	if c.Client == nil {
		c.Client = http.DefaultClient
	}
}

func (c PublisherConfig) validate() error {
	if c.MarshalMessageFunc == nil {
		return ErrNoMarshalFunc
	}

	return nil
}

func NewPublisher(config PublisherConfig, logger watermill.LoggerAdapter) (*Publisher, error) {
	config.setDefaults()
	if err := config.validate(); err != nil {
		return nil, errors.Wrap(err, "invalid Publisher config")
	}
	return &Publisher{
		config: config,
		logger: logger,
	}, nil
}

func (p *Publisher) Publish(topic string, messages ...*message.Message) error {
	if p.closed {
		return ErrPublisherClosed
	}

	for _, msg := range messages {
		req, err := p.config.MarshalMessageFunc(topic, msg)
		if err != nil {
			return errors.Wrapf(err, "cannot marshal message %s", msg.UUID)
		}

		logFields := watermill.LogFields{
			"uuid":     msg.UUID,
			"url":      req.URL.String(),
			"method":   req.Method,
			"provider": ProviderName,
		}

		resp, err := p.config.Client.Do(req)
		if err != nil {
			return errors.Wrapf(err, "publishing message %s failed", msg.UUID)
		}

		p.handleResponseBody(resp, logFields)
		if resp.StatusCode >= http.StatusBadRequest {
			return errors.Wrap(ErrErrorResponse, resp.Status)
		}

		if err != nil {
			return errors.Wrapf(err, "could not close response body for message %s", msg.UUID)
		}

		p.logger.Trace("message published", logFields)
	}

	return nil
}

func (p *Publisher) Close() error {
	if p.closed {
		return nil
	}

	p.closed = true
	return nil
}

func (p Publisher) handleResponseBody(resp *http.Response, logFields watermill.LogFields) {
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusBadRequest {
		return
	}

	if p.config.DoNotLogResponseBodyOnServerError {
		return
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(errors.New("could not read http response"))
	}

	logFields = logFields.Add(watermill.LogFields{
		"http_status":   resp.StatusCode,
		"http_response": string(body),
	})
	p.logger.Info("server responded with error", logFields)
}
