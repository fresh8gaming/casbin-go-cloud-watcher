// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package azservicebus

import (
	"context"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus/internal"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus/internal/amqpwrap"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus/internal/go-amqp"
	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus/internal/utils"
)

type (
	// Sender is used to send messages as well as schedule them to be delivered at a later date.
	Sender struct {
		queueOrTopic   string
		cleanupOnClose func()
		links          internal.AMQPLinks
		retryOptions   RetryOptions
	}
)

// MessageBatchOptions contains options for the `Sender.NewMessageBatch` function.
type MessageBatchOptions struct {
	// MaxBytes overrides the max size (in bytes) for a batch.
	// By default NewMessageBatch will use the max message size provided by the service.
	MaxBytes uint64
}

// NewMessageBatch can be used to create a batch that contain multiple
// messages. Sending a batch of messages is more efficient than sending the
// messages one at a time.
// If the operation fails it can return an *azservicebus.Error type if the failure is actionable.
func (s *Sender) NewMessageBatch(ctx context.Context, options *MessageBatchOptions) (*MessageBatch, error) {
	var batch *MessageBatch

	err := s.links.Retry(ctx, EventSender, "send", func(ctx context.Context, lwid *internal.LinksWithID, args *utils.RetryFnArgs) error {
		maxBytes := lwid.Sender.MaxMessageSize()

		if options != nil && options.MaxBytes != 0 {
			maxBytes = options.MaxBytes
		}

		batch = newMessageBatch(maxBytes)
		return nil
	}, s.retryOptions)

	if err != nil {
		return nil, internal.TransformError(err)
	}

	return batch, nil
}

// SendMessageOptions contains optional parameters for the SendMessage function.
type SendMessageOptions struct {
	// For future expansion
}

// SendMessage sends a Message to a queue or topic.
// If the operation fails it can return an *azservicebus.Error type if the failure is actionable.
func (s *Sender) SendMessage(ctx context.Context, message *Message, options *SendMessageOptions) error {
	err := s.links.Retry(ctx, EventSender, "SendMessage", func(ctx context.Context, lwid *internal.LinksWithID, args *utils.RetryFnArgs) error {
		return lwid.Sender.Send(ctx, message.toAMQPMessage())
	}, RetryOptions(s.retryOptions))

	return internal.TransformError(err)
}

// SendMessageBatchOptions contains optional parameters for the SendMessageBatch function.
type SendMessageBatchOptions struct {
	// For future expansion
}

// SendMessageBatch sends a MessageBatch to a queue or topic.
// Message batches can be created using `Sender.NewMessageBatch`.
// If the operation fails it can return an *azservicebus.Error type if the failure is actionable.
func (s *Sender) SendMessageBatch(ctx context.Context, batch *MessageBatch, options *SendMessageBatchOptions) error {
	err := s.links.Retry(ctx, EventSender, "SendMessageBatch", func(ctx context.Context, lwid *internal.LinksWithID, args *utils.RetryFnArgs) error {
		return lwid.Sender.Send(ctx, batch.toAMQPMessage())
	}, RetryOptions(s.retryOptions))

	return internal.TransformError(err)
}

// ScheduleMessagesOptions contains optional parameters for the ScheduleMessages function.
type ScheduleMessagesOptions struct {
	// For future expansion
}

// ScheduleMessages schedules a slice of Messages to appear on Service Bus Queue/Subscription at a later time.
// Returns the sequence numbers of the messages that were scheduled.  Messages that haven't been
// delivered can be cancelled using `Receiver.CancelScheduleMessage(s)`
// If the operation fails it can return an *azservicebus.Error type if the failure is actionable.
func (s *Sender) ScheduleMessages(ctx context.Context, messages []*Message, scheduledEnqueueTime time.Time, options *ScheduleMessagesOptions) ([]int64, error) {
	var amqpMessages []*amqp.Message

	for _, m := range messages {
		amqpMessages = append(amqpMessages, m.toAMQPMessage())
	}

	ids, err := s.scheduleAMQPMessages(ctx, amqpMessages, scheduledEnqueueTime)
	return ids, internal.TransformError(err)
}

// MessageBatch changes

// CancelScheduledMessagesOptions contains optional parameters for the CancelScheduledMessages function.
type CancelScheduledMessagesOptions struct {
	// For future expansion
}

// CancelScheduledMessages cancels multiple messages that were scheduled.
// If the operation fails it can return an *azservicebus.Error type if the failure is actionable.
func (s *Sender) CancelScheduledMessages(ctx context.Context, sequenceNumbers []int64, options *CancelScheduledMessagesOptions) error {
	err := s.links.Retry(ctx, EventSender, "CancelScheduledMessages", func(ctx context.Context, lwv *internal.LinksWithID, args *utils.RetryFnArgs) error {
		return internal.CancelScheduledMessages(ctx, lwv.RPC, lwv.Sender.LinkName(), sequenceNumbers)
	}, s.retryOptions)

	return internal.TransformError(err)
}

// Close permanently closes the Sender.
func (s *Sender) Close(ctx context.Context) error {
	s.cleanupOnClose()
	return s.links.Close(ctx, true)
}

func (s *Sender) scheduleAMQPMessages(ctx context.Context, messages []*amqp.Message, scheduledEnqueueTime time.Time) ([]int64, error) {
	var sequenceNumbers []int64

	err := s.links.Retry(ctx, EventSender, "ScheduleMessages", func(ctx context.Context, lwv *internal.LinksWithID, args *utils.RetryFnArgs) error {
		sn, err := internal.ScheduleMessages(ctx, lwv.RPC, lwv.Sender.LinkName(), scheduledEnqueueTime, messages)

		if err != nil {
			return err
		}
		sequenceNumbers = sn
		return nil
	}, s.retryOptions)

	return sequenceNumbers, err
}

func (sender *Sender) createSenderLink(ctx context.Context, session amqpwrap.AMQPSession) (internal.AMQPSenderCloser, internal.AMQPReceiverCloser, error) {
	amqpSender, err := session.NewSender(
		amqp.LinkSenderSettle(amqp.ModeMixed),
		amqp.LinkReceiverSettle(amqp.ModeFirst),
		amqp.LinkTargetAddress(sender.queueOrTopic))

	if err != nil {
		return nil, nil, err
	}

	return amqpSender, nil, nil
}

type newSenderArgs struct {
	ns             internal.NamespaceWithNewAMQPLinks
	queueOrTopic   string
	cleanupOnClose func()
	retryOptions   RetryOptions
}

func newSender(args newSenderArgs) (*Sender, error) {
	if err := args.ns.Check(); err != nil {
		return nil, err
	}

	sender := &Sender{
		queueOrTopic:   args.queueOrTopic,
		cleanupOnClose: args.cleanupOnClose,
		retryOptions:   args.retryOptions,
	}

	sender.links = args.ns.NewAMQPLinks(args.queueOrTopic, sender.createSenderLink, internal.GetRecoveryKind)
	return sender, nil
}
