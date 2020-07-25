FROM golang:1.14-alpine
RUN mkdir lelegram
ADD go.sum lelegram
ADD telegram.go lelegram
ADD main.go lelegram
ADD irc lelegram/irc
ADD go.mod lelegram
RUN cd lelegram; go build
ENTRYPOINT ["/go/lelegram/lelelegram"]
