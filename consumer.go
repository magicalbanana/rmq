package rmq

import (
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/streadway/amqp"
)

// Logger ...
type Logger interface {
	Debug(string, ...zapcore.Field)
	Info(string, ...zapcore.Field)
	Warn(string, ...zapcore.Field)
	Error(string, ...zapcore.Field)
	Fatal(string, ...zapcore.Field)
}

// Consumer ...
type Consumer interface {
	AnnounceQueue() (<-chan amqp.Delivery, error)
	Connect() error
	Handle(d <-chan amqp.Delivery, fn func(<-chan amqp.Delivery), threads int)
	Reconnect() (<-chan amqp.Delivery, error)
}

// consumer holds all infromation about the RabbitMQ connection This setup
// does limit a consumer to one exchange. This should not be an issue. Having
// to connect to multiple exchanges means something else is structured
// improperly.
type consumer struct {
	*ConsumerConfig
}

// ConsumerConfig ...
type ConsumerConfig struct {
	done       chan error // the channel that will be used to check whether this should reconnect or not
	connection *amqp.Connection
	channel    *amqp.Channel
	Consume    struct {
		ConsumerTag string
		NoAck       bool
		Exclusive   bool
		NoLocal     bool
		NoWait      bool
		Args        map[string]interface{}
	}
	Exchange struct {
		ExchangeName       string
		ExchangeType       string
		Durable            bool
		DeleteWhenComplete bool
		Internal           bool
		NoWait             bool
		Args               map[string]interface{}
	}
	Queue struct {
		QueueName        string
		Durable          bool
		DeleteWhenUnused bool
		Exclusive        bool
		NoWait           bool
		Args             map[string]interface{}
	}
	QOS struct {
		Count  int
		Size   int
		Global bool
	}
	QueueBind struct {
		ExchangeName string
		RoutingKey   string
		NoWait       bool
		Args         map[string]interface{}
	}
	Logger         Logger
	ReconnectSleep time.Duration // how long before it tries to reconnect
	URI            string        // uri of the rabbitmq server
}

// NewConsumer ...
func NewConsumer(cfg *ConsumerConfig) Consumer {
	return &consumer{cfg}
}

// Reconnect is called in places where NotifyClose() channel is called
// wait 30 seconds before trying to reconnect. Any shorter amount of time
// will  likely destroy the error log while waiting for servers to come
// back online. This requires two parameters which is just to satisfy
// the AccounceQueue call and allows greater flexability
func (c *consumer) Reconnect() (<-chan amqp.Delivery, error) {
	time.Sleep(c.ReconnectSleep)

	err := c.Connect()
	if err != nil {
		c.Logger.Error("Could not connect in reconnect call", zap.String("error", err.Error()))
		return nil, err
	}

	deliveries, err := c.AnnounceQueue()
	if err != nil {
		return deliveries, err
	}

	return deliveries, nil
}

// Connect to RabbitMQ server
func (c *consumer) Connect() error {
	c.done = make(chan error)

	var err error

	c.Logger.Info("Dialing RabbitMQ server")
	c.connection, err = amqp.Dial(c.URI)
	if err != nil {
		return fmt.Errorf("Dial: %s", err)
	}

	go func() {
		// Waits here for the channel to be closed
		e := Error{<-c.connection.NotifyClose(make(chan *amqp.Error))}
		c.Logger.Info("Closing RabbitMQ connection", zap.Object("error", e))
		// Let Handle know it's not time to reconnect
		c.done <- errors.New("channel Closed")
	}()

	c.Logger.Info("got Connection, getting channel")
	c.channel, err = c.connection.Channel()
	if err != nil {
		return fmt.Errorf("channel: %s", err)
	}

	c.Logger.Info("Got channel, declaring Exchange", zap.String("exchange", c.Exchange.ExchangeName))
	err = c.channel.ExchangeDeclare(
		c.Exchange.ExchangeName,       // name of the exchange
		c.Exchange.ExchangeType,       // type
		c.Exchange.Durable,            // durable
		c.Exchange.DeleteWhenComplete, // delete when complete
		c.Exchange.Internal,           // internal
		c.Exchange.NoWait,             // noWait
		c.Exchange.Args,               // arguments
	)
	if err != nil {
		return fmt.Errorf("Exchange Declare: %s", err)
	}

	return nil
}

// AnnounceQueue sets the queue that will be listened to for this
// connection...
func (c *consumer) AnnounceQueue() (<-chan amqp.Delivery, error) {
	c.Logger.Info(
		"declared Exchange, declaring Queue",
		zap.String("queue", c.Queue.QueueName),
		zap.Int("qos_count", c.QOS.Count),
		zap.Int("qos_size", c.QOS.Size),
		zap.Bool("qos_global", c.QOS.Global),
	)

	queue, err := c.channel.QueueDeclare(
		c.Queue.QueueName,        // name of the queue
		c.Queue.Durable,          // durable
		c.Queue.DeleteWhenUnused, // delete when usused
		c.Queue.Exclusive,        // exclusive
		c.Queue.NoWait,           // noWait
		c.Queue.Args,             // arguments
	)

	if err != nil {
		return nil, fmt.Errorf("Queue Declare: %s", err)
	}

	c.Logger.Info(
		"Declared Queue",
		zap.String("queue_name", queue.Name),
		zap.Int("messsages", queue.Messages),
		zap.Int("consumers", queue.Consumers),
		zap.String("routing_key", c.QueueBind.RoutingKey),
	)

	// Qos determines the amount of messages that the queue will pass to you
	// before it waits for you to ack them. This will slow down queue
	// consumption but give you more certainty that all messages are being
	// processed. As load increases I would reccomend upping the about of
	// Threads and Processors the go process uses before changing this
	// although you will eventually need to reach some balance between
	// threads, procs, and Qos.
	err = c.channel.Qos(c.QOS.Count, c.QOS.Size, c.QOS.Global)
	if err != nil {
		return nil, fmt.Errorf("Error setting qos: %s", err)
	}

	err = c.channel.QueueBind(
		queue.Name,               // name of the queue
		c.QueueBind.RoutingKey,   // bindingKey
		c.QueueBind.ExchangeName, // sourceExchange
		c.QueueBind.NoWait,       // noWait
		c.QueueBind.Args,         // arguments
	)
	if err != nil {
		return nil, fmt.Errorf("Queue Bind: %s", err)
	}

	c.Logger.Info("Queue bound to Exchange, starting Consumer", zap.String("consumer_tag", c.Consume.ConsumerTag))
	deliveries, err := c.channel.Consume(
		queue.Name,            // name
		c.Consume.ConsumerTag, // consumerTag,
		c.Consume.NoAck,       // noAck
		c.Consume.Exclusive,   // exclusive
		c.Consume.NoLocal,     // noLocal
		c.Consume.NoWait,      // noWait
		c.Consume.Args,        // arguments
	)
	if err != nil {
		return nil, fmt.Errorf("Queue Consume: %s", err)
	}

	return deliveries, nil
}

// Handle has all the logic to make sure your program keeps running d should
// be a delievey channel as created when you call AnnounceQueue fn should be a
// function that handles the processing of deliveries this should be the last
// thing called in main as code under it will become unreachable unless put
// int a goroutine.
func (c *consumer) Handle(d <-chan amqp.Delivery, fn func(<-chan amqp.Delivery), threads int) {
	// let's declare this error once
	var err error

	for {
		for i := 0; i < threads; i++ {
			go fn(d)
		}

		// Go into reconnect loop when c.done is passed non nil values
		if <-c.done != nil {
			d, err = c.Reconnect()
			if err != nil {
				// Very likely chance of failing should not cause worker to
				// terminate
				c.Logger.Fatal("Failed to reconnect, terminating worker", zap.String("error", err.Error()))
			}
		}

		c.Logger.Info("Reconnected, possibly, I really hope it did")
	}
}
