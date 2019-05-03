package sql

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/pkg/errors"
)

var (
	ErrSubscriberClosed = errors.New("subscriber is closed")
)

type SubscriberConfig struct {
	Logger        watermill.LoggerAdapter
	ConsumerGroup string

	// PollInterval is the interval between subsequent SELECT queries. Must be non-negative. Defaults to 5s.
	PollInterval time.Duration

	// ResendInterval is the time to wait before resending a nacked message. Must be non-negative. Defaults to 1s.
	ResendInterval time.Duration

	// MessagesTable is the name of the table that stores Watermill messages as rows. Defaults to `messages`.
	MessagesTable string
	// MessageOffsetsTable is the name of the table that stores the offsets of messages read by each consumer group.
	// Defaults to `offsets_acked`.
	MessageOffsetsTable string

	// Acker serves to record which messages have already been received by which consumer group.
	Acker Acker
	// Selecter serves to retrieve the Watermill messages from the SQL storage.
	Selecter Selecter
}

func (c *SubscriberConfig) setDefaults() {
	if c.Logger == nil {
		c.Logger = watermill.NopLogger{}
	}
	if c.PollInterval == 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.ResendInterval == 0 {
		c.ResendInterval = time.Second
	}
	if c.MessagesTable == "" {
		c.MessagesTable = "messages"
	}
	if c.MessageOffsetsTable == "" {
		c.MessageOffsetsTable = "offsets_acked"
	}
}

func (c SubscriberConfig) validate() error {
	// TODO: any restraint to prevent really quick polling? I think not, caveat programmator
	if c.PollInterval <= 0 {
		return errors.New("poll interval must be a positive duration")
	}
	if c.ResendInterval <= 0 {
		return errors.New("resend interval must be a positive duration")
	}
	if c.Acker == nil {
		return errors.New("acker is nil")
	}
	if c.Selecter == nil {
		return errors.New("selecter is nil")
	}

	return nil
}

// Subscriber makes SELECT queries on the chosen table with the interval defined in the config.
// The rows are unmarshaled into Watermill messages.
type Subscriber struct {
	db     *sql.DB
	config SubscriberConfig

	subscribeWg *sync.WaitGroup
	closing     chan struct{}
	closed      bool

	ackStmt    *sql.Stmt
	selectStmt *sql.Stmt
}

func NewSubscriber(db *sql.DB, conf SubscriberConfig) (*Subscriber, error) {
	if db == nil {
		return nil, errors.New("db is nil")
	}
	conf.setDefaults()
	err := conf.validate()
	if err != nil {
		return nil, errors.Wrap(err, "invalid config")
	}

	ackStmt, err := db.Prepare(conf.Acker.AckQuery(conf.MessageOffsetsTable, conf.ConsumerGroup))
	if err != nil {
		return nil, errors.Wrap(err, "could not prepare the ack statement")
	}

	selectStmt, err := db.Prepare(conf.Selecter.SelectQuery(conf.MessagesTable, conf.MessageOffsetsTable, conf.ConsumerGroup))
	if err != nil {
		return nil, errors.Wrap(err, "could not prepare the select message statement")
	}

	sub := &Subscriber{
		db:     db,
		config: conf,

		subscribeWg: &sync.WaitGroup{},
		closing:     make(chan struct{}),

		ackStmt:    ackStmt,
		selectStmt: selectStmt,
	}

	return sub, nil
}

func (s *Subscriber) Subscribe(ctx context.Context, topic string) (o <-chan *message.Message, err error) {
	if s.closed {
		return nil, ErrSubscriberClosed
	}

	// the information about closing the subscriber is propagated through ctx
	ctx, cancel := context.WithCancel(ctx)
	out := make(chan *message.Message)

	s.subscribeWg.Add(1)
	go func() {
		s.consume(ctx, topic, out)
		close(out)
		cancel()
	}()

	return out, nil
}

func (s *Subscriber) consume(ctx context.Context, topic string, out chan *message.Message) {
	defer s.subscribeWg.Done()

	logger := s.config.Logger.With(watermill.LogFields{
		"topic":          topic,
		"consumer_group": s.config.ConsumerGroup,
	})

	for {
		select {
		case <-s.closing:
			logger.Info("Discarding queued message, subscriber closing", nil)
			return

		case <-ctx.Done():
			logger.Info("Stopping consume, context canceled", nil)
			return

		default:
			// go on querying
		}

		err := s.query(ctx, topic, out, logger)
		if err != nil {
			logger.Error("Error querying for message", err, nil)
			continue
		}

	}
}

func (s *Subscriber) query(
	ctx context.Context,
	topic string,
	out chan *message.Message,
	logger watermill.LoggerAdapter,
) (err error) {
	// start the transaction
	// it is finalized after the ACK is written
	var tx *sql.Tx
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "could not begin tx for querying")
	}

	defer func() {
		if err != nil {
			rollbackErr := tx.Rollback()
			if rollbackErr != nil {
				logger.Error("could not rollback tx for querying message", rollbackErr, nil)
			}
		} else {
			commitErr := tx.Commit()
			if commitErr != nil {
				logger.Error("could not commit tx for querying message", commitErr, nil)
			}
		}
	}()

	selectStmt := tx.Stmt(s.selectStmt)
	selectArgs, err := s.config.Selecter.SelectArgs(topic)
	if err != nil {
		return errors.Wrap(err, "could not get args for the select query")
	}

	// todo: there might be some args to pass to the query (?) in what case?
	row := selectStmt.QueryRowContext(ctx, selectArgs...)

	var offset int
	var msg *message.Message
	offset, msg, err = s.config.Selecter.UnmarshalMessage(row)
	if errors.Cause(err) == sql.ErrNoRows {
		// wait until polling for the next message
		time.Sleep(s.config.PollInterval)
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "could not unmarshal message from query")
	}

	logger = logger.With(watermill.LogFields{
		"msg_uuid": msg.UUID,
	})

	// todo: different acking strategies?
	acked := s.sendMessage(ctx, msg, out, logger)
	if acked {
		ackStmt := tx.Stmt(s.ackStmt)

		var ackArgs []interface{}
		ackArgs, err = s.config.Acker.AckArgs(offset)
		if err != nil {
			return errors.Wrap(err, "could not get args for acking the message")
		}

		_, err = ackStmt.ExecContext(ctx, ackArgs...)
		if err != nil {
			return errors.Wrap(err, "could not get args for acking the message")
		}
	}

	return nil
}

// sendMessages sends messages on the output channel.
func (s *Subscriber) sendMessage(
	ctx context.Context,
	msg *message.Message,
	out chan *message.Message,
	logger watermill.LoggerAdapter,
) (acked bool) {
	msgCtx, cancel := context.WithCancel(ctx)
	msg.SetContext(msgCtx)
	defer cancel()

ResendLoop:
	for {

		select {
		case out <- msg:

		case <-s.closing:
			logger.Info("Discarding queued message, subscriber closing", nil)
			return false

		case <-ctx.Done():
			logger.Info("Discarding queued message, context canceled", nil)
			return false
		}

		select {
		case <-msg.Acked():
			logger.Debug("Message acked", nil)
			return true

		case <-msg.Nacked():
			//message nacked, try resending
			logger.Debug("Message nacked, resending", nil)
			msg = msg.Copy()

			if s.config.ResendInterval != 0 {
				time.Sleep(s.config.ResendInterval)
			}

			continue ResendLoop

		case <-s.closing:
			logger.Info("Discarding queued message, subscriber closing", nil)
			return false

		case <-ctx.Done():
			logger.Info("Discarding queued message, context canceled", nil)
			return false
		}
	}
}

func (s *Subscriber) Close() error {
	if s.closed {
		return nil
	}

	s.closed = true

	close(s.closing)
	s.subscribeWg.Wait()

	return nil
}