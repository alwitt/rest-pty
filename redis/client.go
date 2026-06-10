package redis

import (
	"context"
	"fmt"

	"github.com/alwitt/goutils"
	"github.com/alwitt/rest-pty/models"
	"github.com/apex/log"
	"github.com/redis/go-redis/v9"
)

// Client to REDIS server
type Client interface {
	/*
		GetRingBuffer prepare data stream ring buffer client.

		The ring buffer maintains the most recent N bytes of data written to it, based on the
		predefined capacity.

			@param ctx context.Context - execution context
			@param bufferName string - buffer name
			@param capacity int64 - buffer max capacity
			@returns the
	*/
	GetRingBuffer(
		ctx context.Context, bufferName string, capacity int64,
	) (RingBuffer, error)

	/*
		DeleteRingBuffer delete a data stream ring buffer

			@param ctx context.Context - execution context
			@param bufferName string - buffer name
			@returns `REDISError` in case of failure
	*/
	DeleteRingBuffer(ctx context.Context, bufferName string) error

	/*
		GetQueueHandle get handle to a REDIS queue

			@param ctx context.Context - execution context
			@returns the queue
	*/
	GetQueueHandle(ctx context.Context, queueName string) (Queue, error)

	/*
		DeleteQueue delete a queue from REDIS

			@param ctx context.Context - execution context
			@param queueName string - queue name
			@returns `REDISError` in case of failure
	*/
	DeleteQueue(ctx context.Context, queueName string) error
}

type clientImpl struct {
	goutils.Component
	serverAddress string
	core          *redis.Client
}

/*
NewClient define a new REDIS client

	@param ctx context.Context - execution context
	@param config common.RedisConnectionConfig - REDIS connection parameters
*/
func NewClient(
	_ context.Context, config models.RedisConnectionConfig,
) (Client, error) {
	serverAddress := fmt.Sprintf("%s:%d", config.Host, config.Port)
	logTags := log.Fields{"module": "redis", "component": "redis-client", "server": serverAddress}

	coreClient := redis.NewClient(&redis.Options{Addr: serverAddress, DB: int(config.DBNumber)})

	if coreClient == nil {
		return nil, fmt.Errorf("failed to define core REDIS client")
	}

	instance := &clientImpl{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		serverAddress: serverAddress,
		core:          coreClient,
	}

	return instance, nil
}

/*
GetRingBuffer prepare data stream ring buffer client.

The ring buffer maintains the most recent N bytes of data written to it, based on the
predefined capacity.

	@param ctx context.Context - execution context
	@param bufferName string - buffer name
	@param capacity int64 - buffer max capacity
	@returns the ring buffer client
	@returns `REDISError` in case of failure
*/
func (c *clientImpl) GetRingBuffer(
	_ context.Context, bufferName string, capacity int64,
) (RingBuffer, error) {
	logTags := log.Fields{
		"module":    "redis",
		"component": "redis-client",
		"server":    c.serverAddress,
		"buffer":    bufferName,
	}

	if capacity < 1 {
		return nil, models.ConsistencyError{Message: "can't create buffer with 0 capacity"}
	}
	if capacity > 512*1024*1024 {
		return nil, models.ConsistencyError{
			Message: "buffer capacity exceeds Redis 512MiB string limit",
		}
	}

	bufferHandle := &ringBuffer{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		bufferNameKey:    bufferName + ".data",
		bufferWrittenKey: bufferName + ".written",
		capacity:         capacity,
		core:             c.core,
	}

	return bufferHandle, nil
}

/*
DeleteRingBuffer delete a data stream ring buffer

	@param ctx context.Context - execution context
	@param bufferName string - buffer name
	@returns `REDISError` in case of failure
*/
func (c *clientImpl) DeleteRingBuffer(ctx context.Context, bufferName string) error {
	bufferKey := bufferName + ".data"
	bufferLenKey := bufferName + ".written"
	resp := c.core.Del(ctx, bufferKey, bufferLenKey)
	if resp.Err() != nil {
		return models.RedisError{
			Core: resp.Err(), Message: "failed to delete buffer " + bufferKey + " and " + bufferLenKey,
		}
	}
	return nil
}

/*
GetQueueHandle get handle to a REDIS queue

	@param ctx context.Context - execution context
	@returns the queue
*/
func (c *clientImpl) GetQueueHandle(
	_ context.Context, queueName string,
) (Queue, error) {
	logTags := log.Fields{
		"module":    "kv",
		"component": "redis-client",
		"server":    c.serverAddress,
		"queue":     queueName,
	}

	queueHandle := &redisQueueImpl{
		Component: goutils.Component{
			LogTags: logTags,
			LogTagModifiers: []goutils.LogMetadataModifier{
				goutils.ModifyLogMetadataByRestRequestParam,
			},
		},
		queueName: queueName,
		core:      c.core,
	}

	return queueHandle, nil
}

/*
DeleteQueue delete a queue from REDIS

	@param ctx context.Context - execution context
	@param queueName string - queue name
	@returns `REDISError` in case of failure
*/
func (c *clientImpl) DeleteQueue(ctx context.Context, queueName string) error {
	resp := c.core.Del(ctx, queueName)
	if resp.Err() != nil {
		return models.RedisError{
			Core: resp.Err(), Message: "failed to delete queue " + queueName,
		}
	}
	return nil
}
