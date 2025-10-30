simulation
----

with docker/podman compose we can scale our application and test the load balancing capabilities
of lug http.RoundTripper.


## requirements

```sh
CGO_ENABLED=0 go build -o okserver/okserver ./okserver

```
