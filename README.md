`impl` generates method stubs for implementing an interface or extracting an interfacce.

```bash
go get -u github.com/jiandahao/impl
```

Sample usage:

- implementing an interface
```bash
$ impl 'f *File' iface io.ReadWriteCloser
func (f *File) Read(p []byte) (n int, err error) {
	panic("not implemented")
}

func (f *File) Write(p []byte) (n int, err error) {
	panic("not implemented")
}

func (f *File) Close() error {
	panic("not implemented")
}

# You can also provide a full name by specifying the package path.
# This helps in cases where the interface can't be guessed
# just from the package name and interface name.
$ impl 's *Source' golang.org/x/oauth2.TokenSource
func (s *Source) Token() (*oauth2.Token, error) {
    panic("not implemented")
}
```

- extracting an interface
```bash
$ impl myinterface struct time.Ticker

type myinterface interface {

	// Stop turns off a ticker. After Stop, no more ticks will be sent.
	// Stop does not close the channel, to prevent a concurrent goroutine
	// reading from the channel from seeing an erroneous "tick".
	Stop()

	// Reset stops a ticker and resets its period to the specified duration.
	// The next tick will arrive after the new period elapses.
	Reset(d time.Duration)
}
```

You can use `impl` from Vim with [vim-go](https://github.com/fatih/vim-go) or
[vim-go-impl](https://github.com/rhysd/vim-go-impl)
