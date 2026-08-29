package redisstore

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct{ URL string }

func New(raw string) *Client { return &Client{URL: raw} }
func (c *Client) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	u, err := url.Parse(c.URL)
	if err != nil {
		return err
	}
	addr := u.Host
	if !strings.Contains(addr, ":") {
		addr += ":6379"
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if u.User != nil {
		if pass, ok := u.User.Password(); ok {
			if err := command(conn, "AUTH", pass); err != nil {
				return err
			}
		}
	}
	db := strings.TrimPrefix(u.Path, "/")
	if db != "" {
		if err := command(conn, "SELECT", db); err != nil {
			return err
		}
	}
	if err := command(conn, "SET", key, string(value), "EX", strconv.FormatInt(int64(ttl/time.Second), 10)); err != nil {
		return err
	}
	return nil
}

func (c *Client) Get(ctx context.Context, key string) ([]byte, error) {
	u, err := url.Parse(c.URL); if err != nil { return nil, err }; addr := u.Host; if !strings.Contains(addr, ":") { addr += ":6379" }
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr); if err != nil { return nil, err }; defer conn.Close()
	if u.User != nil { if pass, ok := u.User.Password(); ok { if err := command(conn, "AUTH", pass); err != nil { return nil, err } } }; db := strings.TrimPrefix(u.Path, "/"); if db != "" { if err := command(conn, "SELECT", db); err != nil { return nil, err } }
	var b strings.Builder; fmt.Fprintf(&b, "*2\r\n$3\r\nGET\r\n$%d\r\n%s\r\n", len(key), key); if _, err := conn.Write([]byte(b.String())); err != nil { return nil, err }
	r := bufio.NewReader(conn); line, err := r.ReadString('\n'); if err != nil { return nil, err }; if strings.HasPrefix(line, "$") { n, _ := strconv.Atoi(strings.TrimSpace(line[1:])); if n < 0 { return nil, fmt.Errorf("redis: key not found") }; data := make([]byte, n+2); if _, err := r.Read(data); err != nil { return nil, err }; return data[:n], nil }; return nil, fmt.Errorf("redis: %s", strings.TrimSpace(line))
}
func command(conn net.Conn, args ...string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.HasPrefix(line, "-") {
		return fmt.Errorf("redis: %s", strings.TrimSpace(line))
	}
	return nil
}
