FROM golang:1.14-alpine
ADD ./lelelegram lelelegram
RUN cd lelelegram && go build .
ENTRYPOINT ./lelelegram/lelelegram
