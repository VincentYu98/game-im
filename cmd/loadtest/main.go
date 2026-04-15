package main

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	pb "game-im/api/pb"
	"game-im/internal/gateway"
)

func main() {
	addr := flag.String("addr", "localhost:9000", "server address")
	totalConns := flag.Int("conns", 10000, "total connections")
	connRate := flag.Int("conn-rate", 500, "connections per second during storm")
	activePct := flag.Float64("active-pct", 0.1, "fraction of connections that are active senders (0.1 = 10%)")
	targetQPS := flag.Int("qps", 20000, "target messages per second from active senders")
	duration := flag.Int("duration", 10, "test duration in seconds after all connections are established")
	flag.Parse()

	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║         Game IM Large-Scale Load Test        ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Printf("Target:       %s\n", *addr)
	fmt.Printf("Connections:  %d\n", *totalConns)
	fmt.Printf("Conn rate:    %d/s\n", *connRate)
	fmt.Printf("Active:       %.0f%% (%d senders)\n", *activePct*100, int(float64(*totalConns)**activePct))
	fmt.Printf("Target QPS:   %d msg/s\n", *targetQPS)
	fmt.Printf("Duration:     %ds\n", *duration)
	fmt.Printf("Goroutines:   %d (before)\n", runtime.NumGoroutine())
	fmt.Println()

	// ═══════════════════════════════════════════════════════
	// Phase 1: Connection Storm
	// ═══════════════════════════════════════════════════════
	fmt.Printf("Phase 1: Connection storm (%d conns at %d/s)...\n", *totalConns, *connRate)

	clients := make([]*ltClient, *totalConns)
	var connOK, connFail atomic.Int64
	var wg sync.WaitGroup

	stormStart := time.Now()
	batchSize := *connRate / 10 // connect in batches every 100ms
	if batchSize < 1 {
		batchSize = 1
	}

	idx := 0
	for idx < *totalConns {
		batchEnd := idx + batchSize
		if batchEnd > *totalConns {
			batchEnd = *totalConns
		}
		for i := idx; i < batchEnd; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				c, err := newLTClient(*addr, int64(id+1))
				if err != nil {
					connFail.Add(1)
					return
				}
				clients[id] = c
				connOK.Add(1)
			}(i)
		}
		idx = batchEnd
		if idx < *totalConns {
			time.Sleep(100 * time.Millisecond)
		}

		// Progress every 2000 connections
		if connOK.Load()%2000 == 0 && connOK.Load() > 0 {
			fmt.Printf("  ... %d/%d connected\n", connOK.Load(), *totalConns)
		}
	}
	wg.Wait()
	stormDur := time.Since(stormStart)

	connected := int(connOK.Load())
	fmt.Printf("  Done: %d/%d connected in %v (%.0f conn/s, %d failed)\n",
		connected, *totalConns, stormDur,
		float64(connected)/stormDur.Seconds(), connFail.Load())
	fmt.Printf("  Goroutines: %d\n", runtime.NumGoroutine())
	fmt.Println()

	if connected == 0 {
		fmt.Println("No connections established. Exiting.")
		os.Exit(1)
	}

	// Split into active vs idle
	numActive := int(float64(connected) * *activePct)
	if numActive < 1 {
		numActive = 1
	}
	activeClients := make([]*ltClient, 0, numActive)
	idleClients := make([]*ltClient, 0, connected-numActive)
	for _, c := range clients {
		if c == nil {
			continue
		}
		if len(activeClients) < numActive {
			activeClients = append(activeClients, c)
		} else {
			idleClients = append(idleClients, c)
		}
	}

	// ═══════════════════════════════════════════════════════
	// Phase 2: Start idle heartbeats
	// ═══════════════════════════════════════════════════════
	fmt.Printf("Phase 2: Starting %d idle heartbeaters + %d active readers...\n",
		len(idleClients), connected)

	stopCh := make(chan struct{})

	// Idle clients: send heartbeat every 30s, read and count WorldNotify
	var totalNotifyRecv atomic.Int64
	for _, c := range idleClients {
		go c.idleLoop(stopCh, &totalNotifyRecv)
	}
	// Active clients also need a reader for WorldNotify
	for _, c := range activeClients {
		go c.readLoop(stopCh, &totalNotifyRecv)
	}

	// Let readers settle
	time.Sleep(200 * time.Millisecond)

	// ═══════════════════════════════════════════════════════
	// Phase 3: Active senders
	// ═══════════════════════════════════════════════════════
	perSenderQPS := float64(*targetQPS) / float64(numActive)
	interval := time.Duration(float64(time.Second) / perSenderQPS)
	testDur := time.Duration(*duration) * time.Second

	fmt.Printf("Phase 3: %d active senders at ~%.0f msg/s each (interval=%v, duration=%v)...\n",
		numActive, perSenderQPS, interval, testDur)

	var totalSent, totalErr atomic.Int64
	var latencies []time.Duration
	var latMu sync.Mutex

	sendStart := time.Now()

	for i, c := range activeClients {
		wg.Add(1)
		go func(client *ltClient, idx int) {
			defer wg.Done()
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			deadline := time.After(testDur)
			seq := 0
			for {
				select {
				case <-deadline:
					return
				case <-ticker.C:
					seq++
					start := time.Now()
					ok := client.sendMsgFireAndForget(idx, seq)
					lat := time.Since(start)
					if ok {
						totalSent.Add(1)
						latMu.Lock()
						latencies = append(latencies, lat)
						latMu.Unlock()
					} else {
						totalErr.Add(1)
					}
				}
			}
		}(c, i)
	}
	wg.Wait()
	sendDur := time.Since(sendStart)

	fmt.Printf("  Sent: %d success, %d errors in %v\n", totalSent.Load(), totalErr.Load(), sendDur)
	fmt.Printf("  Actual QPS: %.0f msg/s\n", float64(totalSent.Load())/sendDur.Seconds())

	// ═══════════════════════════════════════════════════════
	// Phase 4: Broadcast latency measurement
	// ═══════════════════════════════════════════════════════
	fmt.Println()
	fmt.Printf("Phase 4: Broadcast latency test (%d clients)...\n", connected)

	// Wait for pending notifies to settle
	time.Sleep(500 * time.Millisecond)

	// Reset the notify counter
	prevNotify := totalNotifyRecv.Load()

	// Send one message and measure time until all clients receive the WorldNotify
	broadcastStart := time.Now()
	sender := activeClients[0]
	sender.sendMsgFireAndForget(99999, 1)

	// Wait for all non-sender clients to receive the notify
	expectedNotifies := int64(connected - 1) // sender is excluded
	waitDeadline := time.After(10 * time.Second)
	for {
		received := totalNotifyRecv.Load() - prevNotify
		if received >= expectedNotifies {
			break
		}
		select {
		case <-waitDeadline:
			fmt.Printf("  TIMEOUT: only %d/%d clients received notify\n", received, expectedNotifies)
			goto report
		default:
			runtime.Gosched()
		}
	}
	{
		broadcastDur := time.Since(broadcastStart)
		fmt.Printf("  All %d clients received WorldNotify in %v\n", expectedNotifies, broadcastDur)
	}

report:
	// ═══════════════════════════════════════════════════════
	// Phase 5: Report
	// ═══════════════════════════════════════════════════════
	close(stopCh)
	time.Sleep(100 * time.Millisecond)

	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════════╗")
	fmt.Println("║                  Results                     ║")
	fmt.Println("╚══════════════════════════════════════════════╝")
	fmt.Printf("Connections:    %d established / %d target\n", connected, *totalConns)
	fmt.Printf("Storm speed:    %.0f conn/s\n", float64(connected)/stormDur.Seconds())
	fmt.Printf("Active senders: %d (%.0f%%)\n", numActive, *activePct*100)
	fmt.Printf("Messages sent:  %d (%d errors)\n", totalSent.Load(), totalErr.Load())
	fmt.Printf("Actual QPS:     %.0f msg/s\n", float64(totalSent.Load())/sendDur.Seconds())
	fmt.Printf("WorldNotify:    %d received total\n", totalNotifyRecv.Load())
	fmt.Printf("Goroutines:     %d\n", runtime.NumGoroutine())

	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		var sum time.Duration
		for _, l := range latencies {
			sum += l
		}
		fmt.Println()
		fmt.Println("Send latency (fire-and-forget write to TCP):")
		fmt.Printf("  P50:  %v\n", pct(latencies, 50))
		fmt.Printf("  P95:  %v\n", pct(latencies, 95))
		fmt.Printf("  P99:  %v\n", pct(latencies, 99))
		fmt.Printf("  Avg:  %v\n", sum/time.Duration(len(latencies)))
	}

	// Cleanup
	for _, c := range clients {
		if c != nil {
			c.close()
		}
	}
	fmt.Println("\nDone.")
}

func pct(sorted []time.Duration, p int) time.Duration {
	idx := int(math.Ceil(float64(p)/100.0*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ─── Load Test Client ───────────────────────────────────

type ltClient struct {
	conn   net.Conn
	reader *bufio.Reader
	uid    int64
	seqN   atomic.Uint32
	mu     sync.Mutex // protects writes
}

func newLTClient(addr string, uid int64) (*ltClient, error) {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	c := &ltClient{
		conn:   conn,
		reader: bufio.NewReaderSize(conn, 512), // small buffer for idle clients
		uid:    uid,
	}
	if err := c.auth(); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *ltClient) close() {
	c.conn.Close()
}

func (c *ltClient) auth() error {
	req := &pb.AuthReq{Token: fmt.Sprintf("uid:%d", c.uid), Version: "loadtest"}
	payload, _ := proto.Marshal(req)
	if err := c.writeFrame(gateway.CmdAuthReq, payload); err != nil {
		return err
	}
	c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	frame, err := gateway.Decode(c.reader)
	if err != nil {
		return err
	}
	if frame.CmdId != gateway.CmdAuthResp {
		return fmt.Errorf("expected auth resp, got %d", frame.CmdId)
	}
	return nil
}

func (c *ltClient) writeFrame(cmdID uint32, payload []byte) error {
	data, err := gateway.EncodeRaw(cmdID, c.seqN.Add(1), payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = c.conn.Write(data)
	c.mu.Unlock()
	return err
}

// sendMsgFireAndForget sends a message without waiting for response.
// The response will be drained by readLoop.
func (c *ltClient) sendMsgFireAndForget(clientIdx, seq int) bool {
	req := &pb.SendMsgReq{
		ChannelId:   "world",
		ChannelType: pb.ChannelType_CHANNEL_TYPE_WORLD,
		Content:     "load test",
		MsgType:     pb.MsgType_MSG_TYPE_TEXT,
		ClientMsgId: fmt.Sprintf("lt-%d-%d", clientIdx, seq),
	}
	payload, _ := proto.Marshal(req)
	return c.writeFrame(gateway.CmdSendMsgReq, payload) == nil
}

func (c *ltClient) sendHeartbeat() error {
	req := &pb.HeartbeatReq{Timestamp: time.Now().UnixMilli()}
	payload, _ := proto.Marshal(req)
	return c.writeFrame(gateway.CmdHeartbeatReq, payload)
}

// idleLoop sends heartbeats every 30s and reads/counts incoming WorldNotify frames.
func (c *ltClient) idleLoop(stop <-chan struct{}, notifyCount *atomic.Int64) {
	hbTicker := time.NewTicker(10 * time.Second) // heartbeat every 10s to keep alive
	defer hbTicker.Stop()

	go func() {
		for {
			select {
			case <-stop:
				return
			case <-hbTicker.C:
				c.sendHeartbeat()
			}
		}
	}()

	c.drainFrames(stop, notifyCount)
}

// readLoop just reads and counts frames (for active clients whose goroutine sends).
func (c *ltClient) readLoop(stop <-chan struct{}, notifyCount *atomic.Int64) {
	c.drainFrames(stop, notifyCount)
}

func (c *ltClient) drainFrames(stop <-chan struct{}, notifyCount *atomic.Int64) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		c.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		frame, err := gateway.Decode(c.reader)
		if err != nil {
			continue // timeout or error, keep going
		}
		if frame.CmdId == gateway.CmdWorldNotify {
			notifyCount.Add(1)
		}
		// Discard all other frames (HeartbeatResp, SendMsgResp, etc.)
	}
}
