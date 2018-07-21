package rmq

import (
	"github.com/streadway/amqp"
	"go.uber.org/zap/zapcore"
)

// Error wraps the *amqp.Error so that we can log this
type Error struct {
	*amqp.Error
}

// MarshalLogObject satisfies the interface needed so that we can log the
// current Error
func (e Error) MarshalLogObject(kv zapcore.ObjectEncoder) error {
	kv.AddInt("code", e.Code)
	kv.AddString("reason", e.Reason)
	kv.AddBool("recover", e.Recover)
	kv.AddBool("server", e.Server)
	return nil
}
