// Package installtest drives deploy/install.sh end to end on fresh Nodes and
// checks what an operator would see afterwards.
//
// The tests are behind the installtest build tag, because each one boots a
// privileged systemd container (or, on the VM tier, a VM) and installs real
// packages:
//
//	make installtest                                # every supported distro
//	INSTALLTEST_DISTROS=debian-12 make installtest  # one of them
//
// They need Docker on the machine running them and network access to the
// distro and Docker apt repositories. See substrate_test.go for the seam
// between the tests and where Nodes come from.
package installtest
