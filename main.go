package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"

	"kubepack.dev/kubepack/apis/kubepack/v1alpha1"
	"kubepack.dev/kubepack/pkg/lib"

	"github.com/google/uuid"
	flag "github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/yaml"
)

var file = "artifacts/kubedb-community/order.yaml"

func main() {
	flag.StringVar(&file, "file", file, "Path to Order file")
	flag.Parse()

	bs, err := lib.NewTestBlobStore()
	if err != nil {
		klog.Fatal(err)
	}

	data, err := ioutil.ReadFile(file)
	if err != nil {
		klog.Fatal(err)
	}
	var order v1alpha1.Order
	err = yaml.Unmarshal(data, &order)
	if err != nil {
		klog.Fatal(err)
	}
	order.UID = types.UID(uuid.New().String())

	scripts, err := lib.GenerateYAMLScript(bs, lib.DefaultRegistry, order)
	if err != nil {
		klog.Fatal(err)
	}
	data, err = json.MarshalIndent(scripts, "", "  ")
	if err != nil {
		klog.Fatal(err)
	}
	fmt.Println(string(data))
}
