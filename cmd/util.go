package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/fatih/color"
	"github.com/golang/glog"
	"github.com/khulnasoft-lab/kube-bench/check"
	"github.com/spf13/viper"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Print colors
var colors = map[check.State]*color.Color{
	check.PASS: color.New(color.FgGreen),
	check.FAIL: color.New(color.FgRed),
	check.WARN: color.New(color.FgYellow),
	check.INFO: color.New(color.FgBlue),
}

var (
	psFunc          func(string) string
	statFunc        func(string) (os.FileInfo, error)
	getBinariesFunc func(*viper.Viper, check.NodeType) (map[string]string, error)
	TypeMap         = map[string][]string{
		"ca":         {"cafile", "defaultcafile"},
		"kubeconfig": {"kubeconfig", "defaultkubeconfig"},
		"service":    {"svc", "defaultsvc"},
		"config":     {"confs", "defaultconf"},
		"datadir":    {"datadirs", "defaultdatadir"},
	}
)

func init() {
	psFunc = ps
	statFunc = os.Stat
	getBinariesFunc = getBinaries
}

type Platform struct {
	Name    string
	Version string
}

func (p Platform) String() string {
	return fmt.Sprintf("Platform{ Name: %s Version: %s }", p.Name, p.Version)
}

func exitWithError(err error) {
	fmt.Fprintf(os.Stderr, "\n%v\n", err)
	// flush before exit non-zero
	glog.Flush()
	os.Exit(1)
}

func cleanIDs(list string) map[string]bool {
	list = strings.Trim(list, ",")
	ids := strings.Split(list, ",")

	set := make(map[string]bool)

	for _, id := range ids {
		id = strings.Trim(id, " ")
		set[id] = true
	}

	return set
}

// ps execs out to the ps command; it's separated into a function so we can write tests
func ps(proc string) string {
	// TODO: truncate proc to 15 chars
	// See https://github.com/khulnasoft-lab/kube-bench/issues/328#issuecomment-506813344
	glog.V(2).Info(fmt.Sprintf("ps - proc: %q", proc))
	cmd := exec.Command("/bin/ps", "-C", proc, "-o", "cmd", "--no-headers")
	out, err := cmd.Output()
	if err != nil {
		glog.V(2).Info(fmt.Errorf("%s: %s", cmd.Args, err))
	}

	glog.V(2).Info(fmt.Sprintf("ps - returning: %q", string(out)))
	return string(out)
}

// getBinaries finds which of the set of candidate executables are running.
// It returns an error if one mandatory executable is not running.
func getBinaries(v *viper.Viper, nodetype check.NodeType) (map[string]string, error) {
	binmap := make(map[string]string)

	for _, component := range v.GetStringSlice("components") {
		s := v.Sub(component)
		if s == nil {
			continue
		}

		optional := s.GetBool("optional")
		bins := s.GetStringSlice("bins")
		if len(bins) > 0 {
			bin, err := findExecutable(bins)
			if err != nil && !optional {
				glog.V(1).Info(buildComponentMissingErrorMessage(nodetype, component, bins))
				return nil, fmt.Errorf("unable to detect running programs for component %q", component)
			}

			// Default the executable name that we'll substitute to the name of the component
			if bin == "" {
				bin = component
				glog.V(2).Info(fmt.Sprintf("Component %s not running", component))
			} else {
				glog.V(2).Info(fmt.Sprintf("Component %s uses running binary %s", component, bin))
			}
			binmap[component] = bin
		}
	}

	return binmap, nil
}

// getConfigFilePath locates the config files we should be using for CIS version
func getConfigFilePath(benchmarkVersion string, filename string) (path string, err error) {
	glog.V(2).Info(fmt.Sprintf("Looking for config specific CIS version %q", benchmarkVersion))

	path = filepath.Join(cfgDir, benchmarkVersion)
	file := filepath.Join(path, filename)
	glog.V(2).Info(fmt.Sprintf("Looking for file: %s", file))

	if _, err := os.Stat(file); err != nil {
		glog.V(2).Infof("error accessing config file: %q error: %v\n", file, err)
		return "", fmt.Errorf("no test files found <= benchmark version: %s", benchmarkVersion)
	}

	return path, nil
}

// getYamlFilesFromDir returns a list of yaml files in the specified directory, ignoring config.yaml
func getYamlFilesFromDir(path string) (names []string, err error) {
	err = filepath.Walk(path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		_, name := filepath.Split(path)
		if name != "" && name != "config.yaml" && filepath.Ext(name) == ".yaml" {
			names = append(names, path)
		}

		return nil
	})
	return names, err
}

// decrementVersion decrements the version number
// We want to decrement individually even through versions where we don't supply test files
// just in case someone wants to specify their own test files for that version
func decrementVersion(version string) string {
	split := strings.Split(version, ".")
	if len(split) < 2 {
		return ""
	}
	minor, err := strconv.Atoi(split[1])
	if err != nil {
		return ""
	}
	if minor <= 1 {
		return ""
	}
	split[1] = strconv.Itoa(minor - 1)
	return strings.Join(split, ".")
}

// getFiles finds which of the set of candidate files exist
func getFiles(v *viper.Viper, fileType string) map[string]string {
	filemap := make(map[string]string)
	mainOpt := TypeMap[fileType][0]
	defaultOpt := TypeMap[fileType][1]

	for _, component := range v.GetStringSlice("components") {
		s := v.Sub(component)
		if s == nil {
			continue
		}

		// See if any of the candidate files exist
		file := findConfigFile(s.GetStringSlice(mainOpt))
		if file == "" {
			if s.IsSet(defaultOpt) {
				file = s.GetString(defaultOpt)
				glog.V(2).Info(fmt.Sprintf("Using default %s file name '%s' for component %s", fileType, file, component))
			} else {
				// Default the file name that we'll substitute to the name of the component
				glog.V(2).Info(fmt.Sprintf("Missing %s file for %s", fileType, component))
				file = component
			}
		} else {
			glog.V(2).Info(fmt.Sprintf("Component %s uses %s file '%s'", component, fileType, file))
		}

		filemap[component] = file
	}

	return filemap
}

// verifyBin checks that the binary specified is running
func verifyBin(bin string) bool {
	// Strip any quotes
	bin = strings.Trim(bin, "'\"")

	// bin could consist of more than one word
	// We'll search for running processes with the first word, and then check the whole
	// proc as supplied is included in the results
	proc := strings.Fields(bin)[0]
	out := psFunc(proc)

	// There could be multiple lines in the ps output
	// The binary needs to be the first word in the ps output, except that it could be preceded by a path
	// e.g. /usr/bin/kubelet is a match for kubelet
	// but apiserver is not a match for kube-apiserver
	reFirstWord := regexp.MustCompile(`^(\S*\/)*` + bin)
	lines := strings.Split(out, "\n")
	for _, l := range lines {
		glog.V(3).Info(fmt.Sprintf("reFirstWord.Match(%s)", l))
		if reFirstWord.Match([]byte(l)) {
			return true
		}
	}

	return false
}

// fundConfigFile looks through a list of possible config files and finds the first one that exists
func findConfigFile(candidates []string) string {
	for _, c := range candidates {
		_, err := statFunc(c)
		if err == nil {
			return c
		}
		if !os.IsNotExist(err) && !strings.HasSuffix(err.Error(), "not a directory") {
			exitWithError(fmt.Errorf("error looking for file %s: %v", c, err))
		}
	}

	return ""
}

// findExecutable looks through a list of possible executable names and finds the first one that's running
func findExecutable(candidates []string) (string, error) {
	for _, c := range candidates {
		if verifyBin(c) {
			return c, nil
		}
		glog.V(1).Info(fmt.Sprintf("executable '%s' not running", c))
	}

	return "", fmt.Errorf("no candidates running")
}

func multiWordReplace(s string, subname string, sub string) string {
	f := strings.Fields(sub)
	if len(f) > 1 {
		sub = "'" + sub + "'"
	}

	return strings.Replace(s, subname, sub, -1)
}

const missingKubectlKubeletMessage = `
Unable to find the programs kubectl or kubelet in the PATH.
These programs are used to determine which version of Kubernetes is running.
Make sure the /usr/local/mount-from-host/bin directory is mapped to the container,
either in the job.yaml file, or Docker command.

For job.yaml:
...
- name: usr-bin
  mountPath: /usr/local/mount-from-host/bin
...

For docker command:
   docker -v $(which kubectl):/usr/local/mount-from-host/bin/kubectl ....

Alternatively, you can specify the version with --version
   kube-bench --version <VERSION> ...
`

func getKubeVersion() (*KubeVersion, error) {
	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		glog.V(3).Infof("Error fetching cluster config: %s", err)
	}
	isRKE := false
	isAKS := false
	if err == nil && kubeConfig != nil {
		k8sClient, err := kubernetes.NewForConfig(kubeConfig)
		if err != nil {
			glog.V(3).Infof("Failed to fetch k8sClient object from kube config : %s", err)
		}

		if err == nil {
			isRKE, err = IsRKE(context.Background(), k8sClient)
			if err != nil {
				glog.V(3).Infof("Error detecting RKE cluster: %s", err)
			}
			isAKS, err = IsAKS(context.Background(), k8sClient)
			if err != nil {
				glog.V(3).Infof("Error detecting AKS cluster: %s", err)
			}
		}

	}

	if k8sVer, err := getKubeVersionFromRESTAPI(); err == nil {
		glog.V(2).Info(fmt.Sprintf("Kubernetes REST API Reported version: %s", k8sVer))
		if isRKE {
			k8sVer.GitVersion = k8sVer.GitVersion + "-rancher1"
		}
		if isAKS {
			k8sVer.GitVersion = k8sVer.GitVersion + "-aks1"
		}
		return k8sVer, nil
	}

	// These executables might not be on the user's path.
	_, err = exec.LookPath("kubectl")
	if err != nil {
		glog.V(3).Infof("Error locating kubectl: %s", err)
		_, err = exec.LookPath("kubelet")
		if err != nil {
			glog.V(3).Infof("Error locating kubelet: %s", err)
			// Search for the kubelet binary all over the filesystem and run the first match to get the kubernetes version
			cmd := exec.Command("/bin/sh", "-c", "`find / -type f -executable -name kubelet 2>/dev/null | grep -m1 .` --version")
			out, err := cmd.CombinedOutput()
			if err == nil {
				glog.V(3).Infof("Found kubelet and query kubernetes version is: %s", string(out))
				return getVersionFromKubeletOutput(string(out)), nil
			}

			glog.Warning(missingKubectlKubeletMessage)
			glog.V(1).Info("unable to find the programs kubectl or kubelet in the PATH")
			glog.V(1).Infof("Cant detect version, assuming default %s", defaultKubeVersion)
			return &KubeVersion{baseVersion: defaultKubeVersion}, nil
		}
		return getKubeVersionFromKubelet(), nil
	}

	return getKubeVersionFromKubectl(), nil
}

func getKubeVersionFromKubectl() *KubeVersion {
	cmd := exec.Command("kubectl", "version", "-o", "json")
	out, err := cmd.CombinedOutput()
	if err != nil {
		glog.V(2).Infof("Failed to query kubectl: %s", err)
		glog.V(2).Info(err)
	}

	return getVersionFromKubectlOutput(string(out))
}

func getKubeVersionFromKubelet() *KubeVersion {
	cmd := exec.Command("kubelet", "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		glog.V(2).Infof("Failed to query kubelet: %s", err)
		glog.V(2).Info(err)
	}

	return getVersionFromKubeletOutput(string(out))
}

func getVersionFromKubectlOutput(s string) *KubeVersion {
	glog.V(2).Infof("Kubectl output: %s", s)
	type versionResult struct {
		ServerVersion VersionResponse
	}
	vrObj := &versionResult{}
	if err := json.Unmarshal([]byte(s), vrObj); err != nil {
		glog.V(2).Info(err)
		if strings.Contains(s, "The connection to the server") {
			msg := fmt.Sprintf(`Warning: Kubernetes version was not auto-detected because kubectl could not connect to the Kubernetes server. This may be because the kubeconfig information is missing or has credentials that do not match the server. Assuming default version %s`, defaultKubeVersion)
			fmt.Fprintln(os.Stderr, msg)
		}
		glog.V(1).Info(fmt.Sprintf("Unable to get Kubernetes version from kubectl, using default version: %s", defaultKubeVersion))
		return &KubeVersion{baseVersion: defaultKubeVersion}
	}
	sv := vrObj.ServerVersion
	return &KubeVersion{
		Major:      sv.Major,
		Minor:      sv.Minor,
		GitVersion: sv.GitVersion,
	}
}

func getVersionFromKubeletOutput(s string) *KubeVersion {
	glog.V(2).Infof("Kubelet output: %s", s)
	serverVersionRe := regexp.MustCompile(`Kubernetes v(\d+.\d+)`)
	subs := serverVersionRe.FindStringSubmatch(s)
	if len(subs) < 2 {
		glog.V(1).Info(fmt.Sprintf("Unable to get Kubernetes version from kubelet, using default version: %s", defaultKubeVersion))
		return &KubeVersion{baseVersion: defaultKubeVersion}
	}
	return &KubeVersion{baseVersion: subs[1]}
}

func makeSubstitutions(s string, ext string, m map[string]string) (string, []string) {
	substitutions := make([]string, 0)
	for k, v := range m {
		subst := "$" + k + ext
		if v == "" {
			glog.V(2).Info(fmt.Sprintf("No substitution for '%s'\n", subst))
			continue
		}
		glog.V(2).Info(fmt.Sprintf("Substituting %s with '%s'\n", subst, v))
		beforeS := s
		s = multiWordReplace(s, subst, v)
		if beforeS != s {
			substitutions = append(substitutions, v)
		}
	}

	return s, substitutions
}

func isEmpty(str string) bool {
	return strings.TrimSpace(str) == ""
}

func buildComponentMissingErrorMessage(nodetype check.NodeType, component string, bins []string) string {
	errMessageTemplate := `
Unable to detect running programs for component %q
The following %q programs have been searched, but none of them have been found:
%s

These program names are provided in the config.yaml, section '%s.%s.bins'
`

	var componentRoleName, componentType string
	switch nodetype {

	case check.NODE:
		componentRoleName = "worker node"
		componentType = "node"
	case check.ETCD:
		componentRoleName = "etcd node"
		componentType = "etcd"
	default:
		componentRoleName = "master node"
		componentType = "master"
	}

	binList := ""
	for _, bin := range bins {
		binList = fmt.Sprintf("%s\t- %s\n", binList, bin)
	}

	return fmt.Sprintf(errMessageTemplate, component, componentRoleName, binList, componentType, component)
}

func getPlatformInfo() Platform {

	openShiftInfo := getOpenShiftInfo()
	if openShiftInfo.Name != "" && openShiftInfo.Version != "" {
		return openShiftInfo
	}

	kv, err := getKubeVersion()
	if err != nil {
		glog.V(2).Info(err)
		return Platform{}
	}
	return getPlatformInfoFromVersion(kv.GitVersion)
}

func getPlatformInfoFromVersion(s string) Platform {
	versionRe := regexp.MustCompile(`v(\d+\.\d+)\.\d+[-+](\w+)(?:[.\-+]*)\w+`)
	subs := versionRe.FindStringSubmatch(s)
	if len(subs) < 3 {
		return Platform{}
	}
	return Platform{
		Name:    subs[2],
		Version: subs[1],
	}
}

func IsAKS(ctx context.Context, k8sClient kubernetes.Interface) (bool, error) {
	nodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return false, err
	}

	if len(nodes.Items) == 0 {
		return false, nil
	}

	node := nodes.Items[0]
	labels := node.Labels
	if _, exists := labels["kubernetes.azure.com/cluster"]; exists {
		return true, nil
	}

	if strings.HasPrefix(node.Spec.ProviderID, "azure://") {
		return true, nil
	}

	return false, nil
}

func getPlatformBenchmarkVersion(platform Platform) string {
	glog.V(3).Infof("getPlatformBenchmarkVersion platform: %s", platform)
	switch platform.Name {
	case "aks":
		return "aks-1.7"
	case "eks":
		return "eks-1.2.0"
	case "gke":
		switch platform.Version {
		case "1.15", "1.16", "1.17", "1.18", "1.19":
			return "gke-1.0"
		case "1.29", "1.30", "1.31":
			return "gke-1.6.0"
		default:
			return "gke-1.2.0"
		}
	case "aliyun":
		return "ack-1.0"
	case "ocp":
		switch platform.Version {
		case "3.10":
			return "rh-0.7"
		case "4.1":
			return "rh-1.0"
		}
	case "vmware":
		return "tkgi-1.2.53"
	case "k3s":
		switch platform.Version {
		case "1.23":
			return "k3s-cis-1.23"
		case "1.24":
			return "k3s-cis-1.24"
		case "1.25", "1.26", "1.27":
			return "k3s-cis-1.7"
		}
	case "rancher":
		switch platform.Version {
		case "1.23":
			return "rke-cis-1.23"
		case "1.24":
			return "rke-cis-1.24"
		case "1.25", "1.26", "1.27":
			return "rke-cis-1.7"
		}
	case "rke2r":
		switch platform.Version {
		case "1.23":
			return "rke2-cis-1.23"
		case "1.24":
			return "rke2-cis-1.24"
		case "1.25", "1.26", "1.27":
			return "rke2-cis-1.7"
		}
	}
	return ""
}

func getOpenShiftInfo() Platform {
	glog.V(1).Info("Checking for oc")
	_, err := exec.LookPath("oc")

	if err == nil {
		cmd := exec.Command("oc", "version")
		out, err := cmd.CombinedOutput()

		if err == nil {
			versionRe := regexp.MustCompile(`oc v(\d+\.\d+)`)
			subs := versionRe.FindStringSubmatch(string(out))
			if len(subs) < 1 {
				versionRe = regexp.MustCompile(`Client Version:\s*(\d+\.\d+)`)
				subs = versionRe.FindStringSubmatch(string(out))
			}
			if len(subs) > 1 {
				glog.V(2).Infof("OCP output '%s' \nplatform is %s \nocp %v", string(out), getPlatformInfoFromVersion(string(out)), subs[1])
				ocpBenchmarkVersion, err := getOcpValidVersion(subs[1])
				if err == nil {
					return Platform{Name: "ocp", Version: ocpBenchmarkVersion}
				} else {
					glog.V(1).Infof("Can't get getOcpValidVersion: %v", err)
				}
			} else {
				glog.V(1).Infof("Can't parse version output: %v", subs)
			}
		} else {
			glog.V(1).Infof("Can't use oc command: %v", err)
		}
	} else {
		glog.V(1).Infof("Can't find oc command: %v", err)
	}
	return Platform{}
}

func getOcpValidVersion(ocpVer string) (string, error) {
	ocpOriginal := ocpVer

	for !isEmpty(ocpVer) {
		glog.V(3).Info(fmt.Sprintf("getOcpBenchmarkVersion check for ocp: %q \n", ocpVer))
		if ocpVer == "3.10" || ocpVer == "4.1" {
			glog.V(1).Info(fmt.Sprintf("getOcpBenchmarkVersion found valid version for ocp: %q \n", ocpVer))
			return ocpVer, nil
		}
		ocpVer = decrementVersion(ocpVer)
	}

	glog.V(1).Info(fmt.Sprintf("getOcpBenchmarkVersion unable to find a match for: %q", ocpOriginal))
	return "", fmt.Errorf("unable to find a matching Benchmark Version match for ocp version: %s", ocpOriginal)
}

// IsRKE Identifies if the cluster belongs to Rancher Distribution RKE
func IsRKE(ctx context.Context, k8sClient kubernetes.Interface) (bool, error) {
	// if there are windows nodes then this should not be counted as rke.linux
	windowsNodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		Limit:         1,
		LabelSelector: "kubernetes.io/os=windows",
	})
	if err != nil {
		return false, err
	}
	if len(windowsNodes.Items) != 0 {
		return false, nil
	}

	// Any node created by RKE should have the annotation, so just grab 1
	nodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return false, err
	}

	if len(nodes.Items) == 0 {
		return false, nil
	}

	annos := nodes.Items[0].Annotations
	if _, ok := annos["rke.cattle.io/external-ip"]; ok {
		return true, nil
	}
	if _, ok := annos["rke.cattle.io/internal-ip"]; ok {
		return true, nil
	}
	return false, nil
}
