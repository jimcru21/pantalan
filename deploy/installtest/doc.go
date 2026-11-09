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
//
// The VM tier runs the paths only VMs can show on KVM VMs from the
// distros' cloud images (vm_test.go, vm_paths_test.go); without KVM they
// skip:
//
//	make installtest-vm
//
// The VM substrate's pure parts (vmspec_test.go) carry no build tag, so
// their unit tests run in `make check`. So do the installer's decisions
// (install_unit_test.go): install.sh, sourced with PANTALAN_INSTALL_NO_MAIN
// set so it runs nothing, has its host probes replaced by stubs, and each
// decision is called on its own. Rules that are only strings (which
// Dashboard ports are refused, the summary's Firewall line) belong there;
// the container and VM tiers keep one end-to-end path per behaviour, for
// what only a real Node shows.
package installtest
