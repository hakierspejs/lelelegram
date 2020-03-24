FROM golang
ADD ./lelelegram lelelegram
RUN cd lelelegram && go build .
ENTRYPOINT ./lelelegram/lelelegram
