# Kubernetes-mig

This repository contains a complete stateful migration resources for Kubernetes to perform Pod and volume migration.
The repository is composed by two projects implemented in GoLang: the go-agent and the go-server. The go-agent runs inside the container, 
and it is in charge of starting up the container with the overlay filesystem and wrapping the processes with DMTCP.

## Installing

```
go install
```

## Compiling the projects 

```
cd go-server
go mod tidy
go build go-server/main.go

cd ../go-client
go mod tidy
go build -o build/migAgent
```

## Using the go-client inside the container

Add the following layer into the container. [Example here](examples/mosquitto/Dockerfile).

```
COPY --from=golang:1.13-alpine /usr/local/go/ /usr/local/go/
CP go-agent/build/migAgent migAgent
```

## Building the image

Here we use a [mosquitto](examples/mosquitto) application, but you can set up your own using our resources. 
 
```
./examples/mosquitto/build.sh 
```


## How to make the container migration-ready 

Update the container lifecycle. We need to make sure that whenever the container start, it can load 
the Overlay and wrap the process, therefore, we change the postStart routine. Thus, we need to make
sure that whenever the container stops, it has created the checkpoints. 
[Mosquitto deployment example](examples/mosquitto_deployment.yml).

```
lifecycle:
  postStart:
    exec:
      command: [ "./migAgent $root_dir $layers" ]               
  preStop:
    exec:
      command: [ "/migAgent" ]
```


## go-server

**/register**

POST – Add a new pod from request data sent as JSON.
/register/:id+env
returns true if successfully added the pod to the server

**/migrate** 

POST – Start the migration using {deployment:mosquitto, originNode:node01, destNode:node02} data as JSON.
returns the result of the migration

***

## Test and Deploy

Soon the built-in continuous integration in GitLab.

***

## To do

- [x] Create socket communication between containers
- [x] Create mock service to transfer checkpoint files
- [x] Write unit tests for communication
- [ ] Write unit tests for overlay consistency
- [ ] Write unit tests for dmtcp consistency
- [ ] Replace Sockets in server with RESTful API with Go and Gin
- [ ] DMTCP Daemonset

***

## Project status

??