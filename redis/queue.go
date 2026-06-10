package redis

import (
	"context"
	"time"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/models"
	"github.com/redis/go-redis/v9"
)

// peekRightScript atomically read the right-most (tail) element of a list.
//
// LLEN and LINDEX are evaluated within a single atomic script execution, so a concurrent
// operation that alters the queue length can't slip in between reading the length and
// indexing the tail element.
var peekRightScript = redis.NewScript(
	`-- KEYS[1]=queue
-- Returns: tail element, or false (nil) when the queue is empty
local len = redis.call('LLEN', KEYS[1])
if len == 0 then return false end
return redis.call('LINDEX', KEYS[1], len - 1)`,
)

// queueMessage REDIS queue message are all represented as strings
type queueMessage string

// StringPayload return its payload as a string
func (m queueMessage) StringPayload() (string, error) {
	return string(m), nil
}

// Queue REDIS queue manager
type Queue interface {
	// QueueName get the REDIS queue name
	QueueName() string

	/*
		PushRight push message into queue right

			@param ctx context.Context - execution context
			@param message models.QueueMessageEnvelope - message to insert
			@return length of queue after insert
	*/
	PushRight(ctx context.Context, message models.IPCMessageEnvelope) (uint64, error)

	/*
		PushLeft push message into queue left

			@param ctx context.Context - execution context
			@param message models.QueueMessageEnvelope - message to insert
			@return length of queue after insert
	*/
	PushLeft(ctx context.Context, message models.IPCMessageEnvelope) (uint64, error)

	/*
		PopRight pop message from queue right

			@param ctx context.Context - execution context
			@param blocking bool - whether this is a blocking operation
			@param maxWait *time.Duration - if blocking, the max duration. Default 5 sec.
			@return message or nil if empty
	*/
	PopRight(
		ctx context.Context, blocking bool, maxWait *time.Duration,
	) (models.IPCMessageEnvelope, error)

	/*
		PopLeft pop message from queue left

			@param ctx context.Context - execution context
			@param blocking bool - whether this is a blocking operation
			@param maxWait *time.Duration - if blocking, the max duration. Default 5 sec.
			@return message or nil if empty
	*/
	PopLeft(
		ctx context.Context, blocking bool, maxWait *time.Duration,
	) (models.IPCMessageEnvelope, error)

	/*
		PopLeftAndMove pop message from queue left and move that message to another queue.

		On completion, return the popped message.

			@param ctx context.Context - execution context
			@param destination string - destination queue
			@param insertOnLeft bool - whether to insert on left of destination queue. Default is right.
			@param blocking bool - whether this is a blocking operation
			@param maxWait *time.Duration - if blocking, the max duration. Default 5 sec.
			@return message or nil if empty
	*/
	PopLeftAndMove(
		ctx context.Context,
		destination string,
		insertOnLeft bool,
		blocking bool,
		maxWait *time.Duration,
	) (models.IPCMessageEnvelope, error)

	/*
		PopRightAndMove pop message from queue right and move that message to another queue.

		On completion, return the popped message.

			@param ctx context.Context - execution context
			@param destination string - destination queue
			@param insertOnLeft bool - whether to insert on left of destination queue. Default is right.
			@param blocking bool - whether this is a blocking operation
			@param maxWait *time.Duration - if blocking, the max duration. Default 5 sec.
			@return message or nil if empty
	*/
	PopRightAndMove(
		ctx context.Context,
		destination string,
		insertOnLeft bool,
		blocking bool,
		maxWait *time.Duration,
	) (models.IPCMessageEnvelope, error)

	/*
		PeakLeft read the left most value of the queue without removing it

			@param ctx context.Context - execution context
			@return message or nil if empty
	*/
	PeakLeft(ctx context.Context) (models.IPCMessageEnvelope, error)

	/*
		PeakRight read the right most (tail) value of the queue without removing it

			@param ctx context.Context - execution context
			@return message or nil if empty
	*/
	PeakRight(ctx context.Context) (models.IPCMessageEnvelope, error)

	/*
		Length fetch the length of the queue

			@param ctx context.Context - execution context
			@return length of queue
	*/
	Length(ctx context.Context) (uint64, error)

	/*
		Remove delete a message from the queue

			@param ctx context.Context - execution context
			@param message models.QueueMessageEnvelope - the message to delete
	*/
	Remove(ctx context.Context, message models.IPCMessageEnvelope) error
}

type redisQueueImpl struct {
	goutils.Component
	queueName string
	core      *redis.Client
}

// QueueName get the REDIS queue name
func (q *redisQueueImpl) QueueName() string {
	return q.queueName
}

/*
PushRight push message into queue right

	@param ctx context.Context - execution context
	@param message models.QueueMessageEnvelope - message to insert
	@return length of queue after insert
*/
func (q *redisQueueImpl) PushRight(
	ctx context.Context, message models.IPCMessageEnvelope,
) (uint64, error) {
	toInsert, err := message.StringPayload()
	if err != nil {
		return 0, models.RuntimeError{Core: err, Message: "failed to serialize the queue message"}
	}

	resp := q.core.RPush(ctx, q.queueName, toInsert)
	if resp.Err() != nil {
		return 0, models.RedisError{
			Core: resp.Err(), Message: "failed to push right on queue " + q.queueName,
		}
	}

	length, err := resp.Uint64()
	if err != nil {
		return 0, models.RedisError{
			Core: err, Message: "queue " + q.queueName + " length not available after push",
		}
	}

	return length, nil
}

/*
PushLeft push message into queue left

	@param ctx context.Context - execution context
	@param message models.QueueMessageEnvelope - message to insert
	@return length of queue after insert
*/
func (q *redisQueueImpl) PushLeft(
	ctx context.Context, message models.IPCMessageEnvelope,
) (uint64, error) {
	toInsert, err := message.StringPayload()
	if err != nil {
		return 0, models.RuntimeError{Core: err, Message: "failed to serialize the queue message"}
	}

	resp := q.core.LPush(ctx, q.queueName, toInsert)
	if resp.Err() != nil {
		return 0, models.RedisError{
			Core: resp.Err(), Message: "failed to push left on queue " + q.queueName,
		}
	}

	length, err := resp.Uint64()
	if err != nil {
		return 0, models.RedisError{
			Core: err, Message: "queue " + q.queueName + " length not available after push",
		}
	}

	return length, nil
}

/*
PopRight pop message from queue right

	@param ctx context.Context - execution context
	@param blocking bool - whether this is a blocking operation
	@param maxWait *time.Duration - if blocking, the max duration. Default 5 sec.
	@return message or nil if empty
*/
func (q *redisQueueImpl) PopRight(
	ctx context.Context, blocking bool, maxWait *time.Duration,
) (models.IPCMessageEnvelope, error) {
	// Blocking read
	if blocking {
		waitTime := time.Second * 5
		if maxWait != nil {
			waitTime = *maxWait
		}
		resp := q.core.BRPop(ctx, waitTime, q.queueName)
		if resp.Err() != nil {
			if resp.Err() == redis.Nil {
				return nil, nil
			}
			return nil, models.RedisError{
				Core: resp.Err(), Message: "failed to blocking pop queue " + q.queueName + " right",
			}
		}
		allReturns := resp.Val()
		if len(allReturns) != 2 {
			return nil, models.RedisError{
				Core:    resp.Err(),
				Message: "blocking pop queue " + q.queueName + " right response wrong shape",
			}
		}
		return queueMessage(allReturns[1]), nil
	}

	// Non-blocking read
	resp := q.core.RPop(ctx, q.queueName)
	if resp.Err() != nil {
		if resp.Err() == redis.Nil {
			return nil, nil
		}
		return nil, models.RedisError{
			Core: resp.Err(), Message: "failed to pop queue " + q.queueName + " right",
		}
	}

	return queueMessage(resp.Val()), nil
}

/*
PopLeft pop message from queue left

	@param ctx context.Context - execution context
	@param blocking bool - whether this is a blocking operation
	@param maxWait *time.Duration - if blocking, the max duration. Default 5 sec.
	@return message or nil if empty
*/
func (q *redisQueueImpl) PopLeft(
	ctx context.Context, blocking bool, maxWait *time.Duration,
) (models.IPCMessageEnvelope, error) {
	// Blocking read
	if blocking {
		waitTime := time.Second * 5
		if maxWait != nil {
			waitTime = *maxWait
		}
		resp := q.core.BLPop(ctx, waitTime, q.queueName)
		if resp.Err() != nil {
			if resp.Err() == redis.Nil {
				return nil, nil
			}
			return nil, models.RedisError{
				Core: resp.Err(), Message: "failed to blocking pop queue " + q.queueName + " left",
			}
		}
		allReturns := resp.Val()
		if len(allReturns) != 2 {
			return nil, models.RedisError{
				Core:    resp.Err(),
				Message: "blocking pop queue " + q.queueName + " left response wrong shape",
			}
		}
		return queueMessage(allReturns[1]), nil
	}

	// Non-blocking read
	resp := q.core.LPop(ctx, q.queueName)
	if resp.Err() != nil {
		if resp.Err() == redis.Nil {
			return nil, nil
		}
		return nil, models.RedisError{
			Core: resp.Err(), Message: "failed to pop queue " + q.queueName + " left",
		}
	}

	return queueMessage(resp.Val()), nil
}

/*
PopLeftAndMove pop message from queue left and move that message to another queue.

On completion, return the popped message.

	@param ctx context.Context - execution context
	@param destination string - destination queue
	@param insertOnLeft bool - whether to insert on left of destination queue. Default is right.
	@param blocking bool - whether this is a blocking operation
	@param maxWait *time.Duration - if blocking, the max duration. Default 5 sec.
	@return message or nil if empty
*/
func (q *redisQueueImpl) PopLeftAndMove(
	ctx context.Context,
	destination string,
	insertOnLeft bool,
	blocking bool,
	maxWait *time.Duration,
) (models.IPCMessageEnvelope, error) {
	const sourcePos = "LEFT"
	destPos := "RIGHT"
	if insertOnLeft {
		destPos = "LEFT"
	}

	// Blocking pop-and-move
	if blocking {
		waitTime := time.Second * 5
		if maxWait != nil {
			waitTime = *maxWait
		}
		resp := q.core.BLMove(ctx, q.queueName, destination, sourcePos, destPos, waitTime)
		if resp.Err() != nil {
			if resp.Err() == redis.Nil {
				return nil, nil
			}
			return nil, models.RedisError{
				Core: resp.Err(),
				Message: "failed to blocking pop-move queue " +
					q.queueName +
					" " +
					sourcePos +
					" to queue " +
					destination +
					" " +
					destPos,
			}
		}
		return queueMessage(resp.Val()), nil
	}

	// Non-blocking pop-and-mov
	resp := q.core.LMove(ctx, q.queueName, destination, sourcePos, destPos)
	if resp.Err() != nil {
		if resp.Err() == redis.Nil {
			return nil, nil
		}
		return nil, models.RedisError{
			Core: resp.Err(),
			Message: "failed to pop-move queue " +
				q.queueName +
				" " +
				sourcePos +
				" to queue " +
				destination +
				" " +
				destPos,
		}
	}

	return queueMessage(resp.Val()), nil
}

/*
PopRightAndMove pop message from queue right and move that message to another queue.

On completion, return the popped message.

	@param ctx context.Context - execution context
	@param destination string - destination queue
	@param insertOnLeft bool - whether to insert on left of destination queue. Default is right.
	@param blocking bool - whether this is a blocking operation
	@param maxWait *time.Duration - if blocking, the max duration. Default 5 sec.
	@return message or nil if empty
*/
func (q *redisQueueImpl) PopRightAndMove(
	ctx context.Context,
	destination string,
	insertOnLeft bool,
	blocking bool,
	maxWait *time.Duration,
) (models.IPCMessageEnvelope, error) {
	const sourcePos = "RIGHT"
	destPos := "RIGHT"
	if insertOnLeft {
		destPos = "LEFT"
	}

	// Blocking pop-and-move
	if blocking {
		waitTime := time.Second * 5
		if maxWait != nil {
			waitTime = *maxWait
		}
		resp := q.core.BLMove(ctx, q.queueName, destination, sourcePos, destPos, waitTime)
		if resp.Err() != nil {
			if resp.Err() == redis.Nil {
				return nil, nil
			}
			return nil, models.RedisError{
				Core: resp.Err(),
				Message: "failed to blocking pop-move queue " +
					q.queueName +
					" " +
					sourcePos +
					" to queue " +
					destination +
					" " +
					destPos,
			}
		}
		return queueMessage(resp.Val()), nil
	}

	// Non-blocking pop-and-mov
	resp := q.core.LMove(ctx, q.queueName, destination, sourcePos, destPos)
	if resp.Err() != nil {
		if resp.Err() == redis.Nil {
			return nil, nil
		}
		return nil, models.RedisError{
			Core: resp.Err(),
			Message: "failed to pop-move queue " +
				q.queueName +
				" " +
				sourcePos +
				" to queue " +
				destination +
				" " +
				destPos,
		}
	}

	return queueMessage(resp.Val()), nil
}

/*
PeakLeft read the left most value of the queue without removing it

	@param ctx context.Context - execution context
	@return message or nil if empty
*/
func (q *redisQueueImpl) PeakLeft(ctx context.Context) (models.IPCMessageEnvelope, error) {
	resp := q.core.LIndex(ctx, q.queueName, 0)
	if resp.Err() != nil {
		if resp.Err() == redis.Nil {
			return nil, nil
		}
		return nil, models.RedisError{
			Core: resp.Err(), Message: "failed to peak queue " + q.queueName + " left",
		}
	}
	return queueMessage(resp.Val()), nil
}

/*
PeakRight read the right most (tail) value of the queue without removing it.

The length and tail lookup are performed atomically via a LUA script, so a concurrent
operation that changes the queue length can't race between reading the length and
indexing the tail element.

	@param ctx context.Context - execution context
	@return message or nil if empty
*/
func (q *redisQueueImpl) PeakRight(ctx context.Context) (models.IPCMessageEnvelope, error) {
	resp := peekRightScript.Run(ctx, q.core, []string{q.queueName})
	if resp.Err() != nil {
		if resp.Err() == redis.Nil {
			return nil, nil
		}
		return nil, models.RedisError{
			Core: resp.Err(), Message: "failed to peak queue " + q.queueName + " right",
		}
	}

	// An empty queue returns a Lua `false`, which the client surfaces as redis.Nil.
	val, err := resp.Text()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, models.RedisError{
			Core: err, Message: "right peaking queue " + q.queueName + " returned unexpected value",
		}
	}

	return queueMessage(val), nil
}

/*
Length fetch the length of the queue

	@param ctx context.Context - execution context
	@return length of queue
*/
func (q *redisQueueImpl) Length(ctx context.Context) (uint64, error) {
	resp := q.core.LLen(ctx, q.queueName)
	if resp.Err() != nil {
		if resp.Err() == redis.Nil {
			return 0, nil
		}
		return 0, models.RedisError{
			Core: resp.Err(), Message: "failed to get queue " + q.queueName + " length",
		}
	}

	length, err := resp.Uint64()
	if err != nil {
		return 0, models.RedisError{
			Core: err, Message: "queue " + q.queueName + " length not returned",
		}
	}

	return length, nil
}

/*
Remove delete a message from the queue

	@param ctx context.Context - execution context
	@param message models.QueueMessageEnvelope - the message to delete
*/
func (q *redisQueueImpl) Remove(ctx context.Context, message models.IPCMessageEnvelope) error {
	toDelete, err := message.StringPayload()
	if err != nil {
		return models.RuntimeError{Core: err, Message: "failed to serialize the queue message"}
	}

	resp := q.core.LRem(ctx, q.queueName, 0, toDelete)
	if resp.Err() != nil {
		if resp.Err() == redis.Nil {
			return nil
		}
		return models.RedisError{
			Core: resp.Err(), Message: "failed to delete from queue " + q.queueName,
		}
	}
	deleted := resp.Val()
	if deleted <= 0 {
		return models.RedisError{
			Message: "failed to delete message '" + toDelete + "' from queue " + q.queueName,
		}
	}

	return nil
}
