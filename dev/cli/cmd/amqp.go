package cmd

import (
	"github.com/ThreeDotsLabs/watermill/message/infrastructure/amqp"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// amqpCmd is a mid-level command for working with the AMQP Pub/Sub provider.
var amqpCmd = &cobra.Command{
	Use:   "amqp",
	Short: "Commands for the AMQP Pub/Sub provider",
	Long: `Consume or produce messages from the AMQP Pub/Sub provider.

For the configuration of consuming/producing of the messages, check the help of the relevant command.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		err := rootCmd.PersistentPreRunE(cmd, args)
		if err != nil {
			return err
		}

		logger.Debug("Using AMQP Pub/Sub", nil)

		if cmd.Use == "consume" {
			// amqp is special
			topic = viper.GetString("amqp.consume.queue")
			consumer, err = amqp.NewSubscriber(amqpConsumerConfig(), logger)
			if err != nil {
				return err
			}
		}

		if cmd.Use == "produce" {
			// amqp is special
			topic = viper.GetString("amqp.produce.exchange")
			producer, err = amqp.NewPublisher(amqpProducerConfig(), logger)
			if err != nil {
				return err
			}
		}

		return nil
	},
}

func amqpConsumerConfig() amqp.Config {
	uri := viper.GetString("amqp.uri")
	queue := viper.GetString("amqp.consume.queue")
	exchangeName := viper.GetString("amqp.consume.exchange")
	exchangeType := viper.GetString("amqp.produce.exchangeType")
	durable := viper.GetBool("amqp.durable")

	return amqp.Config{
		Connection: amqp.ConnectionConfig{
			AmqpURI: uri,
		},
		Marshaler: amqp.DefaultMarshaler{},
		Queue: amqp.QueueConfig{
			GenerateName: func(topic string) string {
				return queue
			},
			Durable: durable,
		},
		Consume: amqp.ConsumeConfig{
			Qos: amqp.QosConfig{
				PrefetchCount: 1,
			},
		},

		Exchange: amqp.ExchangeConfig{
			GenerateName: func(topic string) string {
				return exchangeName
			},
			Type:    exchangeType,
			Durable: durable,
		},
	}
}

func amqpProducerConfig() amqp.Config {
	uri := viper.GetString("amqp.uri")
	exchangeName := viper.GetString("amqp.produce.exchange")
	exchangeType := viper.GetString("amqp.produce.exchangeType")
	routingKey := viper.GetString("amqp.produce.routingKey")
	durable := viper.GetBool("amqp.durable")

	return amqp.Config{
		Connection: amqp.ConnectionConfig{
			AmqpURI: uri,
		},
		Marshaler: amqp.DefaultMarshaler{},
		Exchange: amqp.ExchangeConfig{
			GenerateName: func(topic string) string {
				return exchangeName
			},
			Type:    exchangeType,
			Durable: durable,
		},
		Publish: amqp.PublishConfig{
			GenerateRoutingKey: func(topic string) string {
				return routingKey
			},
		},
	}
}

func init() {
	// Here you will define your flags and configuration settings.
	rootCmd.AddCommand(amqpCmd)
	configureAmqpCmd()
	consumeCmd := addConsumeCmd(amqpCmd, false)
	configureConsumeCmd(consumeCmd)
	produceCmd := addProduceCmd(amqpCmd, false)
	configureProduceCmd(produceCmd)
}

func configureAmqpCmd() {
	amqpCmd.PersistentFlags().StringP(
		"uri",
		"u",
		"",
		"The URI to the AMQP instance (required)",
	)
	if err := amqpCmd.MarkPersistentFlagRequired("uri"); err != nil {
		panic(err)
	}
	if err := viper.BindPFlag("amqp.uri", amqpCmd.PersistentFlags().Lookup("uri")); err != nil {
		panic(err)
	}

	amqpCmd.PersistentFlags().Bool(
		"durable",
		true,
		"If true, the queues and exchanges created automatically (if any) will be durable",
	)
	if err := viper.BindPFlag("amqp.durable", amqpCmd.PersistentFlags().Lookup("durable")); err != nil {
		panic(err)
	}
}

func configureConsumeCmd(consumeCmd *cobra.Command) {
	consumeCmd.PersistentFlags().StringP(
		"queue",
		"q",
		"",
		"The name of the AMQP queue to consume messages from (required)",
	)
	if err := consumeCmd.MarkPersistentFlagRequired("queue"); err != nil {
		panic(err)
	}
	if err := viper.BindPFlag("amqp.consume.queue", consumeCmd.PersistentFlags().Lookup("queue")); err != nil {
		panic(err)
	}

	consumeCmd.PersistentFlags().StringP(
		"exchange",
		"x",
		"",
		"If non-empty, an exchange with this name is created if it didn't exist. Then, the queue is bound to this exchange.",
	)
	if err := viper.BindPFlag("amqp.consume.exchange", consumeCmd.PersistentFlags().Lookup("exchange")); err != nil {
		panic(err)
	}

	consumeCmd.PersistentFlags().String(
		"exchangeType",
		"fanout",
		"If exchange needs to be created, it will be created with this type. The common types are 'direct', 'fanout', 'topic' and 'headers'.",
	)
	if err := consumeCmd.MarkPersistentFlagRequired("exchange"); err != nil {
		panic(err)
	}

	if err := viper.BindPFlag("amqp.produce.exchangeType", consumeCmd.PersistentFlags().Lookup("exchangeType")); err != nil {
		panic(err)
	}
}

func configureProduceCmd(produceCmd *cobra.Command) {
	produceCmd.PersistentFlags().StringP(
		"exchange",
		"x",
		"",
		"The name of the AMQP exchange to produce messages to (required)",
	)
	if err := produceCmd.MarkPersistentFlagRequired("exchange"); err != nil {
		panic(err)
	}
	if err := viper.BindPFlag("amqp.produce.exchange", produceCmd.PersistentFlags().Lookup("exchange")); err != nil {
		panic(err)
	}

	produceCmd.PersistentFlags().String(
		"exchangeType",
		"fanout",
		"If the exchange did not exist, it will be created with this type. The common types are 'direct', 'fanout', 'topic' and 'headers'.",
	)
	if err := produceCmd.MarkPersistentFlagRequired("exchange"); err != nil {
		panic(err)
	}

	if err := viper.BindPFlag("amqp.produce.exchangeType", produceCmd.PersistentFlags().Lookup("exchangeType")); err != nil {
		panic(err)
	}

	produceCmd.PersistentFlags().StringP(
		"routingKey",
		"r",
		"",
		"The routing key to use when publishing the message.",
	)
	if err := produceCmd.MarkPersistentFlagRequired("routingKey"); err != nil {
		panic(err)
	}
	if err := viper.BindPFlag("amqp.produce.routingKey", produceCmd.PersistentFlags().Lookup("routingKey")); err != nil {
		panic(err)
	}
}
