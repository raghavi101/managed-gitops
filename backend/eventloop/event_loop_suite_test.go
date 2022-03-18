package eventloop

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestEventLoop(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Event Loop Tests")
}
