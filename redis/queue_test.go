package redis_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alwitt/rest-pty/models"
	"github.com/alwitt/rest-pty/redis"
	"github.com/apex/log"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// testIPCMessage a minimal `models.IPCMessageEnvelope` implementation used to drive the
// queue unit tests.
type testIPCMessage struct {
	payload string
}

// StringPayload return its payload as a string
func (m testIPCMessage) StringPayload() (string, error) {
	return m.payload, nil
}

func TestRedisQueueBasicPushPop(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	redisConnect := getRedisConnectParamForTest(assert)
	client, err := redis.NewClient(utCtx, redisConnect)
	assert.Nil(err)

	testQueue := uuid.NewString()

	uut, err := client.GetQueueHandle(utCtx, testQueue)
	assert.Nil(err)
	defer func() {
		assert.Nil(client.DeleteQueue(utCtx, testQueue))
	}()

	// Case 0: an empty queue returns nothing for peak and pop operations
	{
		msg, err := uut.PeakLeft(utCtx)
		assert.Nil(err)
		assert.Nil(msg)

		msg, err = uut.PeakRight(utCtx)
		assert.Nil(err)
		assert.Nil(msg)

		msg, err = uut.PopLeft(utCtx, false, nil)
		assert.Nil(err)
		assert.Nil(msg)

		msg, err = uut.PopRight(utCtx, false, nil)
		assert.Nil(err)
		assert.Nil(msg)
	}

	// Case 1: standard use
	testMsg1 := testIPCMessage{payload: uuid.NewString()}
	testMsg2 := testIPCMessage{payload: uuid.NewString()}
	{
		// Push message 1 on the left, then message 2 on the right. The queue is now
		// [message 1, message 2] from left to right.
		length, err := uut.PushLeft(utCtx, testMsg1)
		assert.Nil(err)
		assert.Equal(uint64(1), length)

		length, err = uut.PushRight(utCtx, testMsg2)
		assert.Nil(err)
		assert.Equal(uint64(2), length)

		// Peak both ends without modifying the queue
		left, err := uut.PeakLeft(utCtx)
		assert.Nil(err)
		assert.NotNil(left)
		leftPayload, err := left.StringPayload()
		assert.Nil(err)
		assert.Equal(testMsg1.payload, leftPayload)

		right, err := uut.PeakRight(utCtx)
		assert.Nil(err)
		assert.NotNil(right)
		rightPayload, err := right.StringPayload()
		assert.Nil(err)
		assert.Equal(testMsg2.payload, rightPayload)

		// Pop the right, which removes message 2
		popped, err := uut.PopRight(utCtx, false, nil)
		assert.Nil(err)
		assert.NotNil(popped)
		poppedPayload, err := popped.StringPayload()
		assert.Nil(err)
		assert.Equal(testMsg2.payload, poppedPayload)

		// Peaking the right now returns message 1, the only remaining message
		right, err = uut.PeakRight(utCtx)
		assert.Nil(err)
		assert.NotNil(right)
		rightPayload, err = right.StringPayload()
		assert.Nil(err)
		assert.Equal(testMsg1.payload, rightPayload)
	}

	// Case 2: length change. Repeatedly push messages on the right and verify the queue
	// length grows by one with each push. The queue currently holds one message (message 1).
	{
		baseLength, err := uut.Length(utCtx)
		assert.Nil(err)
		assert.Equal(uint64(1), baseLength)

		for i := 1; i <= 10; i++ {
			length, err := uut.PushRight(utCtx, testIPCMessage{payload: uuid.NewString()})
			assert.Nil(err)
			assert.Equal(baseLength+uint64(i), length)

			observed, err := uut.Length(utCtx)
			assert.Nil(err)
			assert.Equal(baseLength+uint64(i), observed)
		}
	}
}

func TestRedisQueueBlockingPop(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	redisConnect := getRedisConnectParamForTest(assert)
	client, err := redis.NewClient(utCtx, redisConnect)
	assert.Nil(err)

	const messageCount = 5
	const writeInterval = time.Millisecond * 10
	// the blocking pop may have to wait for the writer between messages, so allow a generous
	// per-message timeout
	maxWait := time.Second * 2

	// runScenario drives one blocking-pop scenario: a reader goroutine drains `messageCount`
	// messages from the queue using the provided blocking `pop`, while the main thread writes
	// them in order via `PushRight` with a small delay between writes. It then asserts the
	// messages were read back in the order they were written.
	runScenario := func(
		pop func(q redis.Queue) (models.IPCMessageEnvelope, error),
	) {
		testQueue := uuid.NewString()
		uut, err := client.GetQueueHandle(utCtx, testQueue)
		assert.Nil(err)
		defer func() {
			assert.Nil(client.DeleteQueue(utCtx, testQueue))
		}()

		received := make([]string, 0, messageCount)
		var wg sync.WaitGroup
		wg.Add(1)

		// Reader thread: block-pop `messageCount` messages off the queue
		go func() {
			defer wg.Done()
			for i := 0; i < messageCount; i++ {
				msg, err := pop(uut)
				assert.Nil(err)
				assert.NotNil(msg)
				if msg == nil {
					return
				}
				payload, err := msg.StringPayload()
				assert.Nil(err)
				received = append(received, payload)
			}
		}()

		// Writer (main thread): push `messageCount` messages, pausing between each so the
		// reader is forced to block and wake on each new arrival
		expected := make([]string, 0, messageCount)
		for i := 0; i < messageCount; i++ {
			msg := testIPCMessage{payload: uuid.NewString()}
			expected = append(expected, msg.payload)
			_, err := uut.PushRight(utCtx, msg)
			assert.Nil(err)
			time.Sleep(writeInterval)
		}

		wg.Wait()

		// All messages were received in the order they were written
		assert.Equal(expected, received)
	}

	// Case 0: blocking PopLeft. The writer pushes on the right and the reader pops the left,
	// so the messages come back in FIFO order.
	{
		runScenario(func(q redis.Queue) (models.IPCMessageEnvelope, error) {
			return q.PopLeft(utCtx, true, &maxWait)
		})
	}

	// Case 1: blocking PopRight. The writer keeps pace with the reader (one message in flight
	// at a time), so each message is popped from the right before the next is pushed, again
	// yielding the written order.
	{
		runScenario(func(q redis.Queue) (models.IPCMessageEnvelope, error) {
			return q.PopRight(utCtx, true, &maxWait)
		})
	}
}

func TestRedisQueueBasicPopAndMove(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	redisConnect := getRedisConnectParamForTest(assert)
	client, err := redis.NewClient(utCtx, redisConnect)
	assert.Nil(err)

	// Define two test queues
	testQueue0 := uuid.NewString()
	testQueue1 := uuid.NewString()

	queue0, err := client.GetQueueHandle(utCtx, testQueue0)
	assert.Nil(err)
	defer func() {
		assert.Nil(client.DeleteQueue(utCtx, testQueue0))
	}()

	queue1, err := client.GetQueueHandle(utCtx, testQueue1)
	assert.Nil(err)
	defer func() {
		assert.Nil(client.DeleteQueue(utCtx, testQueue1))
	}()

	// verifyPayload helper to assert a popped/peaked message carries the expected payload
	verifyPayload := func(msg models.IPCMessageEnvelope, expected string) {
		assert.NotNil(msg)
		payload, err := msg.StringPayload()
		assert.Nil(err)
		assert.Equal(expected, payload)
	}

	// Push right entries 0 to 4 into queue 0. Queue 0 is now [0, 1, 2, 3, 4] left to right.
	testEntries := make([]testIPCMessage, 5)
	for i := range testEntries {
		testEntries[i] = testIPCMessage{payload: uuid.NewString()}
		_, err := queue0.PushRight(utCtx, testEntries[i])
		assert.Nil(err)
	}

	// Pop left on queue 0 (entry 0) and move to queue 1 left. Queue 1 is now [0].
	{
		moved, err := queue0.PopLeftAndMove(utCtx, testQueue1, true, false, nil)
		assert.Nil(err)
		verifyPayload(moved, testEntries[0].payload)

		left, err := queue1.PeakLeft(utCtx)
		assert.Nil(err)
		verifyPayload(left, testEntries[0].payload)
	}

	// Pop right on queue 0 (entry 4) and move to queue 1 right. Queue 1 is now [0, 4].
	{
		moved, err := queue0.PopRightAndMove(utCtx, testQueue1, false, false, nil)
		assert.Nil(err)
		verifyPayload(moved, testEntries[4].payload)

		right, err := queue1.PeakRight(utCtx)
		assert.Nil(err)
		verifyPayload(right, testEntries[4].payload)
	}

	// Pop right on queue 0 (entry 3) and move to queue 1 left. Queue 1 is now [3, 0, 4].
	{
		moved, err := queue0.PopRightAndMove(utCtx, testQueue1, true, false, nil)
		assert.Nil(err)
		verifyPayload(moved, testEntries[3].payload)

		left, err := queue1.PeakLeft(utCtx)
		assert.Nil(err)
		verifyPayload(left, testEntries[3].payload)
	}

	// Pop right on queue 1 (entry 4) and move to queue 0 left. Queue 0 is now [4, 1, 2].
	{
		moved, err := queue1.PopRightAndMove(utCtx, testQueue0, true, false, nil)
		assert.Nil(err)
		verifyPayload(moved, testEntries[4].payload)

		left, err := queue0.PeakLeft(utCtx)
		assert.Nil(err)
		verifyPayload(left, testEntries[4].payload)
	}
}

func TestRedisQueueBlockingPopAndMove(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	redisConnect := getRedisConnectParamForTest(assert)
	client, err := redis.NewClient(utCtx, redisConnect)
	assert.Nil(err)

	const messageCount = 5
	const writeInterval = time.Millisecond * 10
	// the blocking pop-and-move may have to wait for the writer between messages, so allow a
	// generous per-message timeout
	maxWait := time.Second * 2

	// runScenario drives one blocking pop-and-move scenario: a mover goroutine drains
	// `messageCount` messages from queue 0 into queue 1 using the provided blocking `move`,
	// while the main thread writes them in order into queue 0 via `PushRight` with a small
	// delay between writes. It then asserts the messages were moved (and landed in queue 1)
	// in the order they were written.
	runScenario := func(
		move func(src redis.Queue, destName string) (models.IPCMessageEnvelope, error),
	) {
		testQueue0 := uuid.NewString()
		testQueue1 := uuid.NewString()

		queue0, err := client.GetQueueHandle(utCtx, testQueue0)
		assert.Nil(err)
		defer func() {
			assert.Nil(client.DeleteQueue(utCtx, testQueue0))
		}()

		queue1, err := client.GetQueueHandle(utCtx, testQueue1)
		assert.Nil(err)
		defer func() {
			assert.Nil(client.DeleteQueue(utCtx, testQueue1))
		}()

		moved := make([]string, 0, messageCount)
		var wg sync.WaitGroup
		wg.Add(1)

		// Mover thread: block pop-and-move `messageCount` messages from queue 0 to queue 1
		go func() {
			defer wg.Done()
			for i := 0; i < messageCount; i++ {
				msg, err := move(queue0, testQueue1)
				assert.Nil(err)
				assert.NotNil(msg)
				if msg == nil {
					return
				}
				payload, err := msg.StringPayload()
				assert.Nil(err)
				moved = append(moved, payload)
			}
		}()

		// Writer (main thread): push `messageCount` messages into queue 0, pausing between
		// each so the mover is forced to block and wake on each new arrival
		expected := make([]string, 0, messageCount)
		for i := 0; i < messageCount; i++ {
			msg := testIPCMessage{payload: uuid.NewString()}
			expected = append(expected, msg.payload)
			_, err := queue0.PushRight(utCtx, msg)
			assert.Nil(err)
			time.Sleep(writeInterval)
		}

		wg.Wait()

		// The mover observed all messages in the order they were written
		assert.Equal(expected, moved)

		// Queue 1 holds every message, in the written order from left to right
		length, err := queue1.Length(utCtx)
		assert.Nil(err)
		assert.Equal(uint64(messageCount), length)
		for i := 0; i < messageCount; i++ {
			msg, err := queue1.PopLeft(utCtx, false, nil)
			assert.Nil(err)
			assert.NotNil(msg)
			if msg == nil {
				continue
			}
			payload, err := msg.StringPayload()
			assert.Nil(err)
			assert.Equal(expected[i], payload)
		}
	}

	// Case 0: blocking PopLeftAndMove. The writer pushes on the right of queue 0 and the mover
	// pops the left and inserts on the right of queue 1, so the messages land in FIFO order.
	{
		runScenario(func(src redis.Queue, destName string) (models.IPCMessageEnvelope, error) {
			return src.PopLeftAndMove(utCtx, destName, false, true, &maxWait)
		})
	}

	// Case 1: blocking PopRightAndMove. The writer keeps pace with the mover (one message in
	// flight at a time), so each message is popped from the right of queue 0 and inserted on
	// the right of queue 1 before the next is pushed, again yielding the written order.
	{
		runScenario(func(src redis.Queue, destName string) (models.IPCMessageEnvelope, error) {
			return src.PopRightAndMove(utCtx, destName, false, true, &maxWait)
		})
	}
}

func TestRedisQueueRemoveEntries(t *testing.T) {
	assert := assert.New(t)
	log.SetLevel(log.DebugLevel)

	utCtx := context.Background()

	redisConnect := getRedisConnectParamForTest(assert)
	client, err := redis.NewClient(utCtx, redisConnect)
	assert.Nil(err)

	testQueue := uuid.NewString()

	uut, err := client.GetQueueHandle(utCtx, testQueue)
	assert.Nil(err)
	defer func() {
		assert.Nil(client.DeleteQueue(utCtx, testQueue))
	}()

	// Push 5 messages into the queue. The queue is now [0, 1, 2, 3, 4] left to right.
	testEntries := make([]testIPCMessage, 5)
	for i := range testEntries {
		testEntries[i] = testIPCMessage{payload: uuid.NewString()}
		_, err := uut.PushRight(utCtx, testEntries[i])
		assert.Nil(err)
	}

	// Remove messages 2 and 4. The queue is now [0, 1, 3] left to right.
	assert.Nil(uut.Remove(utCtx, testEntries[2]))
	assert.Nil(uut.Remove(utCtx, testEntries[4]))

	// The queue length is now 3
	length, err := uut.Length(utCtx)
	assert.Nil(err)
	assert.Equal(uint64(3), length)

	// Pop left all remaining messages and verify they are messages 0, 1, and 3 in order
	for _, expected := range []testIPCMessage{testEntries[0], testEntries[1], testEntries[3]} {
		msg, err := uut.PopLeft(utCtx, false, nil)
		assert.Nil(err)
		assert.NotNil(msg)
		payload, err := msg.StringPayload()
		assert.Nil(err)
		assert.Equal(expected.payload, payload)
	}

	// The queue is now empty
	length, err = uut.Length(utCtx)
	assert.Nil(err)
	assert.Equal(uint64(0), length)
}
