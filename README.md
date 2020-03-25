lelelegram
----------

**Lelelegram** is a fork of [lelegram](https://code.hackerspace.pl/hscloud/tree/personal/q3k/lelegram), irc/telegram bridge of [hackerspace warsaw](https://hackerspace.pl/). The main purpose of this project is to get rid of dependencies specific to the architecture of [hscloud](https://code.hackerspace.pl/hscloud/) and simplify building process with usage of `go mod`.

## Building

In order to build **lelelegram** you need `go 1.14` and `git` installed on your machine. If these conditions are met, you can use the following command:

    $ go build

Binary executable should appear in your directory. Now you can check possible usage with below command:

    $ ./lelelegram --help
