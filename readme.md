lug: a go http 1.1 roundtripper which support multiple hosts load balancing

## notes (with http 1.1 keep-alive)

1. go `net.Dial` returns a connection with `TCP Keep-Alive` packet
2. go `http server` will response with `TCP Keep-Alive ACK` packet
   but without new request on the same connection, go `http server` will close the connection
   after `http server`'s `IdleTimeout.` A packet `FIN, ACK` will be sent to client.
   note, an opened (not used) connection will be keep alived up to `http server`'s `ReadTimeout`.
   if the client doesn't close it, the connection will stay as `FIN-WAIT-2` on the server side.
3. in lug.RoundTripper, there can be
   1. i/o timeout:    dial before dns refresh
   2. unexpected EOF: when the server closed during reading response

   lug won't retry on these errors.
4. concurrent for requests way larger than MaxIdleConn, lug is slower than http.DefaultTransport
5. sequencial requests, lug is faster than http.DefaultTransport
6. lug has built-in load balancing
