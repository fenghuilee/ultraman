package server

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"ultramand/lib/log"
	"ultramand/lib/ssdb"

	httpserv "ultramand/lib/conn/http"
	websocketserv "ultramand/lib/conn/websocket"

	"github.com/gorilla/websocket"
	"github.com/seefan/gossdb"
)

var domainList map[string]string                  //[domain]user
var userList map[string](*(websocketserv.Client)) //[user]websocket

var wg sync.WaitGroup
var httpServer *httpserv.Server
var websockServer *websocketserv.Server
var ssdbClient *gossdb.Client

func startServer(httpAddr, webSocketAddr, ssdbAddr string) {
	domainList = make(map[string]string)
	userList = make(map[string](*(websocketserv.Client)))

	ssdbAddrs := strings.Split(ssdbAddr, ":")
	ssdbHost := ssdbAddrs[0]
	ssdbPort, _ := strconv.ParseInt(ssdbAddrs[1], 10, 32)
	ssdbClient = ssdb.Run(ssdbHost, int(ssdbPort))

	wg.Add(1)
	go buildHttpServer(&wg, httpAddr)

	wg.Add(1)
	go buildWebSocketServer(&wg, webSocketAddr)

	wg.Wait()
}

func buildHttpServer(wg *(sync.WaitGroup), addr string) {
	defer (*wg).Done()

	httpServer = httpserv.New(addr)

	httpServer.OnNewClient(func(c *(httpserv.Client)) {
		// new client connected
		// lets send some message
		log.Debug("New http connection: %s", (*(c.Conn)).RemoteAddr().String())
		httpServer.Clients[(*(c.Conn)).RemoteAddr().String()] = c
		log.Debug("Total %d http connection(s) connected", len(httpServer.Clients))
	})

	httpServer.OnNewRequest(func(c *(httpserv.Client), message []byte) {
		// new http request message received
		proxyHttpRequest(c, &message)
	})

	httpServer.OnClientClosed(func(c *(httpserv.Client)) {
		// connection with client lost
		log.Debug("Http connection closed: %s", (*(c.Conn)).RemoteAddr().String())
		delete(httpServer.Clients, (*(c.Conn)).RemoteAddr().String())
		log.Debug("Total %d http connection(s) connected", len(httpServer.Clients))
	})

	httpServer.Listen()
}

// Handles a new http connection from the public internet
func proxyHttpRequest(c *(httpserv.Client), message *([]byte)) {
	headers := strings.Split(string(*message), "\n")
	domain := strings.TrimSpace((strings.Split(headers[1], ":"))[1])
	user, ok := domainList[domain]

	if ok == false {
		(*(c.Conn)).Write([]byte(fmt.Sprintf(NotFound, len(domain)+18, domain)))
		c.Close()
		return
	}

	id := []string{(*(c.Conn)).RemoteAddr().String()}
	newHeaders := make([]string, 1+len(headers))
	copy(newHeaders, id)
	copy(newHeaders[1:], headers)

	wsc := userList[user]

	wsc.Conn.WriteMessage(websocket.BinaryMessage, []byte(strings.Join(newHeaders, "\n")))
}

func buildWebSocketServer(wg *(sync.WaitGroup), addr string) {
	defer (*wg).Done()

	websockServer = websocketserv.New(addr)

	websockServer.OnNewClient(func(c *(websocketserv.Client)) {
		// new client connected
		// lets send some message
		log.Debug("New websocket connection: %s", c.Conn.RemoteAddr().String())
		websockServer.Clients[c.Conn.RemoteAddr().String()] = c
		log.Debug("Total %d websocket connection(s) connected", len(websockServer.Clients))

		var wgca sync.WaitGroup
		wgca.Add(1)
		go handleClientAuth(&wgca, c)
		wgca.Wait()

	})

	websockServer.OnNewRequest(func(c *(websocketserv.Client)) {
		// new http request message received
	})

	websockServer.OnNewRespone(func(c *(websocketserv.Client), message []byte) {
		// new http request message received
		handleHttpRespone(&message)
	})

	websockServer.OnClientClosed(func(c *(websocketserv.Client), err error) {
		// connection with client lost
		log.Debug("Websocket connection closed: %s", c.Conn.RemoteAddr().String())
		delete(websockServer.Clients, c.Conn.RemoteAddr().String())
		log.Debug("Total %d http connection(s) connected", len(websockServer.Clients))
	})

	websockServer.Listen()
}

func handleClientAuth(wg *(sync.WaitGroup), c *(websocketserv.Client)) {
	defer (*wg).Done()
	log.Debug("Wait client %s login", c.Conn.RemoteAddr().String())
	c.Conn.WriteMessage(websocket.TextMessage, []byte("Please login"))
	for {
		_, message, err := c.Conn.ReadMessage()
		if err != nil {
			log.Debug("Failed to login: %s, %v", c.Conn.RemoteAddr().String(), err)
			return
		}
		log.Debug("Received auth message: %s, %s", c.Conn.RemoteAddr().String(), message)

		authMessage := strings.Split(string(message), ":")

		if len(authMessage) != 2 {
			log.Debug("Error auth message: %s, %s", c.Conn.RemoteAddr().String(), message)
			c.Conn.WriteMessage(websocket.TextMessage, []byte("Error auth message"))
			c.Conn.Close()
			return
		}

		user := authMessage[0]
		key := authMessage[1]

		rst, err := ssdbClient.Get(user)

		if err != nil {
			log.Warn("SSDB Error: %v", err)
			c.Conn.WriteMessage(websocket.TextMessage, []byte("System error, please try again latter"))
			c.Conn.Close()
			return
		} else {
			if rst.String() != key {
				log.Debug("Error auth message: %s, %s", c.Conn.RemoteAddr().String(), message)
				c.Conn.WriteMessage(websocket.TextMessage, []byte("Error auth message"))
				c.Conn.Close()
				return
			}
		}

		userList[user] = c

		rsts, err := ssdbClient.Hscan(user, "", "", 5)
		domainLocalHostPort := []string{}
		if err != nil {
			log.Warn("SSDB Error: %v", err)
			c.Conn.WriteMessage(websocket.TextMessage, []byte("System error, please try again latter"))
			c.Conn.Close()
			return
		} else {
			for domain, lhp := range rsts {
				domainLocalHostPort = append(domainLocalHostPort, domain+"|"+lhp.String())
				domainList[domain] = user
			}
		}

		c.Conn.WriteMessage(websocket.TextMessage, []byte("ok"))

		c.Conn.WriteMessage(websocket.TextMessage, []byte(strings.Join(domainLocalHostPort, "\n")))
		break
	}
}

func handleHttpRespone(message *([]byte)) {
	idx := bytes.Index(*message, []byte("\n"))
	id := string((*message)[0:idx])
	respMsg := (*message)[idx:]

	c := *((*(httpServer.Clients[id])).Conn)
	c.Write(respMsg)
}
