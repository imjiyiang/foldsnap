package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"localbackup/internal/backup"
	"localbackup/internal/server"
	"localbackup/internal/store"
)

func main() {
	startupAt := time.Now()
	log.Printf("startup: process entered")
	home, _ := os.UserHomeDir()
	addr := flag.String("addr", "127.0.0.1:8787", "Loopback listen address")
	data := flag.String("data", filepath.Join(home, "Library", "Application Support", "LocalBackup"), "Private data directory")
	web := flag.String("web", "", "Optional external web assets (default: embedded)")
	dev := flag.Bool("dev", false, "Allow Vite dev origin on localhost:5173")
	exe, _ := os.Executable()
	appLaunch := strings.Contains(exe, ".app/Contents/MacOS/")
	open := flag.Bool("open", appLaunch, "Open browser")
	autoPort := flag.Bool("auto-port", appLaunch, "Use an available loopback port if the requested port is occupied")
	flag.Parse()
	host, _, e := net.SplitHostPort(*addr)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("服务只能监听 loopback IP")
	}
	if e = os.MkdirAll(*data, 0700); e != nil {
		log.Fatal(e)
	}
	os.Chmod(*data, 0700)
	lock, e := os.OpenFile(filepath.Join(*data, "service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		log.Fatal(e)
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		url, err := existingServiceURL(*data, 5*time.Second)
		if err != nil {
			log.Fatalf("这个数据目录已有服务，但无法连接：%v", err)
		}
		fmt.Println("Foldsnap 已启动：" + url)
		if *open {
			if err := exec.Command("open", url).Run(); err != nil {
				log.Fatal(err)
			}
		}
		return
	}
	tokenPath := filepath.Join(*data, "access.token")
	b, e := os.ReadFile(tokenPath)
	if os.IsNotExist(e) {
		b = []byte(backup.ID(""))
		e = os.WriteFile(tokenPath, b, 0600)
	}
	if e != nil {
		log.Fatal(e)
	}
	token := strings.TrimSpace(string(b))
	if len(token) < 24 {
		log.Fatal("访问令牌无效")
	}
	listener, e := listenLocal(*addr, *autoPort)
	if e != nil {
		log.Fatalf("无法监听 %s（端口可能已被其他程序占用）：%v", *addr, e)
	}
	defer listener.Close()
	*addr = listener.Addr().String()
	log.Printf("startup: opening database (%s)", time.Since(startupAt))
	st, e := store.Open(filepath.Join(*data, "localbackup.sqlite"))
	if e != nil {
		log.Fatal(e)
	}
	defer st.DB.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("startup: database ready (%s)", time.Since(startupAt))
	s := server.New(st, token, *addr, *web, *dev)
	if e = writeEndpoint(*data, *addr); e != nil {
		log.Fatal(e)
	}
	defer os.Remove(filepath.Join(*data, "service-endpoint.json"))
	s.Start(ctx)
	httpServer := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	serveResult := make(chan error, 1)
	go func() { serveResult <- httpServer.Serve(listener) }()
	log.Printf("startup: HTTP ready (%s)", time.Since(startupAt))
	url := "http://" + *addr + "/#token=" + token
	fmt.Println("Foldsnap 已启动：" + url)
	if *dev {
		fmt.Println("开发界面：http://127.0.0.1:5173/#token=" + token)
	}
	if *open {
		if e := exec.Command("open", url).Run(); e != nil {
			log.Printf("无法打开浏览器：%v", e)
		}
	}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		httpServer.Shutdown(c)
	}()
	if e = <-serveResult; e != nil && e != http.ErrServerClosed {
		log.Fatal(e)
	}
}
