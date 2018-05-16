FROM golang:latest AS build
WORKDIR /go/src/app
COPY . .
RUN go get k8s.io/client-go/...
RUN go get -d -v ./...
RUN CGO_ENABLED=0 GOOS=linux go build -o fortigate-lb-ctlr *.go

FROM scratch
COPY --from=build /go/src/app/fortigate-lb-ctlr /fortigate-lb-ctlr
CMD ["/fortigate-lb-ctlr"]
