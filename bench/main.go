package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	sharddnsv1 "github.com/Adatage/ShardDNS/api/sharddns/v1"
	"github.com/Adatage/ShardDNS/api/sharddns/v1/sharddnsv1connect"
	"github.com/Adatage/ShardDNS/internal/dns"

	"connectrpc.com/connect"
)

func main() {
	var (
		server   = flag.String("server", "127.0.0.1:53", "DNS server address")
		api      = flag.String("api", "http://127.0.0.1:9053", "API address used by -seed")
		token    = flag.String("token", os.Getenv("SHARDDNS_TOKEN"), "API bearer token")
		zone     = flag.String("zone", "bench.test", "zone to query or seed")
		names    = flag.Int("names", 10000, "number of distinct host names")
		seed     = flag.Bool("seed", false, "create the zone and records through the API, then exit")
		duration = flag.Duration("duration", 10*time.Second, "test duration")
		conc     = flag.Int("c", 64, "concurrent clients")
		nxRatio  = flag.Float64("nx", 0.05, "fraction of queries for names that do not exist")
		zipf     = flag.Bool("zipf", true, "skewed (zipf) name popularity instead of uniform")
		tcp      = flag.Bool("tcp", false, "use TCP")
	)
	flag.Parse()

	if *seed {
		if err := seedZone(*api, *token, *zone, *names); err != nil {
			fmt.Fprintln(os.Stderr, "seed:", err)
			os.Exit(1)
		}
		return
	}
	run(*server, *zone, *names, *duration, *conc, *nxRatio, *zipf, *tcp)
}

func seedZone(api, token, zone string, n int) error {
	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", "Bearer "+token)
				return next(ctx, req)
			}
		})))
	}
	client := sharddnsv1connect.NewDNSManagerClient(&http.Client{Timeout: 30 * time.Second}, api, opts...)
	ctx := context.Background()

	_, err := client.CreateZone(ctx, connect.NewRequest(&sharddnsv1.CreateZoneRequest{Name: zone}))
	if err != nil && connect.CodeOf(err) != connect.CodeAlreadyExists {
		return err
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	var failed atomic.Int64
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				_, err := client.CreateRecord(ctx, connect.NewRequest(&sharddnsv1.CreateRecordRequest{
					Zone: zone, Name: fmt.Sprintf("host%d", i), Type: "A", Ttl: 300,
					Rdata: fmt.Sprintf("10.%d.%d.%d", (i>>16)&255, (i>>8)&255, i&255),
				}))
				if err != nil {
					failed.Add(1)
				}
			}
		}()
	}
	start := time.Now()
	for i := 0; i < n; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	fmt.Printf("seeded %d records in %s (%d failed)\n", n, time.Since(start).Round(time.Millisecond), failed.Load())
	return nil
}

func run(server, zone string, names int, duration time.Duration, conc int, nxRatio float64, useZipf, useTCP bool) {
	var (
		sent, ok, nx, bad, timeouts atomic.Int64
		mu                          sync.Mutex
		all                         []time.Duration
	)
	deadline := time.Now().Add(duration)
	var wg sync.WaitGroup
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(id)+1, uint64(time.Now().UnixNano())))
			var z *rand.Zipf
			if useZipf {
				z = rand.NewZipf(rng, 1.2, 1, uint64(names-1))
			}
			network := "udp"
			if useTCP {
				network = "tcp"
			}
			conn, err := net.Dial(network, server)
			if err != nil {
				fmt.Fprintln(os.Stderr, "dial:", err)
				return
			}
			defer conn.Close()

			lat := make([]time.Duration, 0, 1<<16)
			buf := make([]byte, 4096)
			out := make([]byte, 0, 512)
			var qid uint16
			for time.Now().Before(deadline) {
				var idx int
				if z != nil {
					idx = int(z.Uint64())
				} else {
					idx = rng.IntN(names)
				}
				name := fmt.Sprintf("host%d.%s", idx, zone)
				wantNX := rng.Float64() < nxRatio
				if wantNX {
					name = fmt.Sprintf("missing%d.%s", rng.IntN(1<<30), zone)
				}
				qid++
				q := &dns.Message{Header: dns.Header{ID: qid, Flags: dns.FlagRD}, Questions: []dns.Question{{Name: name, Type: dns.TypeA, Class: dns.ClassIN}}}
				q.SetEDNS0(1232, false)
				wire, _ := q.Pack(out[:0])

				start := time.Now()
				_ = conn.SetDeadline(start.Add(2 * time.Second))
				var n int
				if useTCP {
					frame := append([]byte{byte(len(wire) >> 8), byte(len(wire))}, wire...)
					_, err = conn.Write(frame)
					if err == nil {
						n, err = readTCP(conn, buf)
					}
				} else {
					_, err = conn.Write(wire)
					if err == nil {
						n, err = conn.Read(buf)
					}
				}
				sent.Add(1)
				if err != nil {
					timeouts.Add(1)
					continue
				}
				lat = append(lat, time.Since(start))
				resp, perr := dns.ParseMessage(buf[:n])
				switch {
				case perr != nil || resp.ID != qid:
					bad.Add(1)
				case resp.Rcode() == dns.RcodeNXDomain:
					nx.Add(1)
				case resp.Rcode() == dns.RcodeNoError && len(resp.Answers) > 0:
					ok.Add(1)
				default:
					bad.Add(1)
				}
			}
			mu.Lock()
			all = append(all, lat...)
			mu.Unlock()
		}(w)
	}
	wg.Wait()

	slices.Sort(all)
	pct := func(p float64) time.Duration {
		if len(all) == 0 {
			return 0
		}
		return all[min(len(all)-1, int(float64(len(all))*p))]
	}
	total := float64(len(all))
	fmt.Printf("queries sent:   %d\n", sent.Load())
	fmt.Printf("answered ok:    %d\n", ok.Load())
	fmt.Printf("nxdomain:       %d\n", nx.Load())
	fmt.Printf("bad responses:  %d\n", bad.Load())
	fmt.Printf("timeouts/errs:  %d\n", timeouts.Load())
	fmt.Printf("throughput:     %.0f qps\n", total/duration.Seconds())
	fmt.Printf("latency p50:    %s\n", pct(0.50))
	fmt.Printf("latency p95:    %s\n", pct(0.95))
	fmt.Printf("latency p99:    %s\n", pct(0.99))
	fmt.Printf("latency max:    %s\n", pct(1))
}

func readTCP(conn net.Conn, buf []byte) (int, error) {
	var hdr [2]byte
	if _, err := readFull(conn, hdr[:]); err != nil {
		return 0, err
	}
	n := int(hdr[0])<<8 | int(hdr[1])
	if n > len(buf) {
		return 0, fmt.Errorf("response too large")
	}
	return readFull(conn, buf[:n])
}

func readFull(conn net.Conn, b []byte) (int, error) {
	got := 0
	for got < len(b) {
		n, err := conn.Read(b[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}
