package dumper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
)

// Dumper struct is for dumping cluster
type Dumper struct {
	cmd           string
	kubeconfig    string
	resources     []string
	filePaths     []string
	fileContainer string
	namespace     string
	location      string
	errors        string
	mode          int64
	crType        string
	forwardport   string
}

var resourcesRe = regexp.MustCompile(`(\w+)\.(\w+).percona\.com`)

// New return new Dumper object
func New(location, namespace, resource string, kubeconfig string, forwardport string) Dumper {
	d := Dumper{
		cmd:         "kubectl",
		kubeconfig:  kubeconfig,
		location:    "cluster-dump",
		mode:        int64(0o777),
		namespace:   namespace,
		forwardport: forwardport,
	}
	resources := []string{
		"pods",
		"replicasets",
		"deployments",
		"statefulsets",
		"replicationcontrollers",
		"events",
		"configmaps",
		"cronjobs",
		"jobs",
		"poddisruptionbudgets",
		"clusterrolebindings",
		"clusterroles",
		"rolebindings",
		"roles",
		"storageclasses",
		"persistentvolumeclaims",
		"persistentvolumes",
	}

	switch resource {
	case "auto":
		result, err := d.runCmd("api-resources", "-o", "name")
		if err != nil {
			log.Panicf("Cannot get API resources and option --resource=auto specified:\n%s", err)
		}
		matches := resourcesRe.FindAllStringSubmatch(string(result), -1)
		if len(matches) == 0 {
			resource = "none"
			break
		}
		for _, match := range matches {
			resources = append(resources, match[1])
			resource = match[2]
		}
	case "pg":
		resources = append(resources,
			"perconapgclusters",
			"pgclusters",
			"pgpolicies",
			"pgreplicas",
			"pgtasks",
		)
	case "pgv2":
		resources = append(resources,
			"perconapgbackups",
			"perconapgclusters",
			"perconapgrestores",
		)
	case "pxc":
		resources = append(resources,
			"perconaxtradbclusterbackups",
			"perconaxtradbclusterrestores",
			"perconaxtradbclusters",
		)
	case "ps":
		resources = append(resources,
			"perconaservermysqlbackups",
			"perconaservermysqlrestores",
			"perconaservermysqls",
		)
	case "psmdb":
		resources = append(resources,
			"perconaservermongodbbackups",
			"perconaservermongodbrestores",
			"perconaservermongodbs",
		)
	}
	filePaths := make([]string, 0)
	if resourceType(resource) == "pxc" {
		filePaths = append(filePaths,
			"var/lib/mysql/mysqld-error.log",
			"var/lib/mysql/innobackup.backup.log",
			"var/lib/mysql/innobackup.move.log",
			"var/lib/mysql/innobackup.prepare.log",
			"var/lib/mysql/grastate.dat",
			"var/lib/mysql/gvwstate.dat",
			"var/lib/mysql/mysqld.post.processing.log",
			"var/lib/mysql/auto.cnf",
		)
		d.fileContainer = "logs"
	}
	d.resources = resources
	d.crType = resource
	d.filePaths = filePaths
	return d
}

type k8sPods struct {
	Items []corev1.Pod `json:"items"`
}

type namespaces struct {
	Items []corev1.Namespace `json:"items"`
}

// DumpCluster create dump of a cluster in Dumper.location
func (d *Dumper) DumpCluster() error {
	file, err := os.Create(d.location + ".tar.gz")
	if err != nil {
		return errors.Wrap(err, "create tar file")
	}

	zr := gzip.NewWriter(file)
	tw := tar.NewWriter(zr)
	defer func() {
		err = addToArchive(d.location+"/errors.txt", d.mode, []byte(d.errors), tw)
		if err != nil {
			log.Println("Error: add errors.txt to archive:", err)
		}

		err = tw.Close()
		if err != nil {
			log.Println("close tar writer", err)
			return
		}
		err = zr.Close()
		if err != nil {
			log.Println("close gzip writer", err)
			return
		}
		err = file.Close()
		if err != nil {
			log.Println("close file", err)
			return
		}
	}()

	var nss namespaces

	if len(d.namespace) > 0 {
		ns := corev1.Namespace{}
		ns.Name = d.namespace
		nss.Items = append(nss.Items, ns)
	} else {
		args := []string{"get", "namespaces", "-o", "json"}
		output, err := d.runCmd(args...)
		if err != nil {
			d.logError(err.Error(), args...)
			return errors.Wrap(err, "get namespaces")
		}

		err = json.Unmarshal(output, &nss)
		if err != nil {
			d.logError(err.Error(), "unmarshal namespaces")
			return errors.Wrap(err, "unmarshal namespaces")
		}
	}

	for _, ns := range nss.Items {
		args := []string{"get", "pods", "-o", "json", "--namespace", ns.Name}
		output, err := d.runCmd(args...)
		if err != nil {
			d.logError(err.Error(), args...)
			continue
		}

		var pods k8sPods
		err = json.Unmarshal(output, &pods)
		if err != nil {
			d.logError(err.Error(), "unmarshal pods from namespace", ns.Name)
			log.Printf("Error: unmarshal pods in namespace %s: %v", ns.Name, err)
		}

		for _, pod := range pods.Items {
			location := filepath.Join(d.location, ns.Name, pod.Name, "logs.txt")
			args := []string{"logs", pod.Name, "--namespace", ns.Name, "--all-containers"}
			output, err = d.runCmd(args...)
			if err != nil {
				d.logError(err.Error(), args...)
				err = addToArchive(location, d.mode, []byte(err.Error()), tw)
				if err != nil {
					log.Printf("Error: create archive with logs for pod %s in namespace %s: %v", pod.Name, ns.Name, err)
				}
				continue
			}
			err = addToArchive(location, d.mode, output, tw)
			if err != nil {
				d.logError(err.Error(), "create archive for pod "+pod.Name)
				log.Printf("Error: create archive for pod %s: %v", pod.Name, err)
			}
			if len(pod.Labels) == 0 {
				continue
			}
			location = filepath.Join(d.location, ns.Name, pod.Name, "/summary.txt")
			component := resourceType(d.crType)
			if component == "psmdb" {
				component = "mongod"
			}
			if component == "ps" {
				component = "mysql"
			}
			if pod.Labels["app.kubernetes.io/instance"] != "" && pod.Labels["app.kubernetes.io/component"] != "" {
				resource := "secret/" + pod.Labels["app.kubernetes.io/instance"] + "-" + pod.Labels["app.kubernetes.io/component"]
				err = d.getResource(resource, ns.Name, true, tw)
				if err != nil {
					log.Printf("Error: get %s resource: %v", resource, err)
				}
			}
			if pod.Labels["app.kubernetes.io/component"] == component ||
				(component == "pg" && pod.Labels["pgo-pg-database"] == "true") ||
				(component == "pgv2" && pod.Labels["pgv2.percona.com/version"] != "" && pod.Labels["postgres-operator.crunchydata.com/instance"] != "") {
				var crName string
				if component == "pg" {
					crName = pod.Labels["pg-cluster"]
				} else if component == "pgv2" {
					crName = pod.Labels["postgres-operator.crunchydata.com/cluster"]
				} else {
					crName = pod.Labels["app.kubernetes.io/instance"]
				}
				// Get summary
				output, err = d.getPodSummary(resourceType(d.crType), pod.Name, crName, ns.Name, tw)
				if err != nil {
					d.logError(err.Error(), d.crType, pod.Name)
					err = addToArchive(location, d.mode, []byte(err.Error()), tw)
					if err != nil {
						log.Printf("Error: create summary errors archive for pod %s in namespace %s: %v", pod.Name, ns.Name, err)
					}
				} else {
					err = addToArchive(location, d.mode, output, tw)
					if err != nil {
						d.logError(err.Error(), "create summary archive for pod "+pod.Name)
						log.Printf("Error: create summary  archive for pod %s: %v", pod.Name, err)
					}
				}

				// get individual Logs
				location = filepath.Join(d.location, ns.Name, pod.Name)
				for _, path := range d.filePaths {
					err = d.getIndividualFiles(ns.Name, pod.Name, path, location, tw)
					if err != nil {
						d.logError(err.Error(), "get file "+path+" for pod "+pod.Name)
						log.Printf("Error: get %s file: %v", path, err)
					}
				}
			}
		}

		for _, resource := range d.resources {
			err = d.getResource(resource, ns.Name, false, tw)
			if err != nil {
				log.Printf("Error: get %s resource: %v", resource, err)
			}
		}
	}

	err = d.getResource("nodes", "", false, tw)
	if err != nil {
		return errors.Wrapf(err, "get nodes")
	}

	return nil
}

// runCmd run command (Dumper.cmd) with given args, return it output
func (d *Dumper) runCmd(args ...string) ([]byte, error) {
	var outb, errb bytes.Buffer
	args = append(args, "--kubeconfig", d.kubeconfig)
	cmd := exec.Command(d.cmd, args...)
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil || errb.Len() > 0 {
		return nil, errors.Errorf("error: %v, stderr: %s, stdout: %s", err, errb.String(), outb.String())
	}

	return outb.Bytes(), nil
}

func (d *Dumper) getResource(name, namespace string, ignoreNotFound bool, tw *tar.Writer) error {
	location := d.location
	args := []string{"get", name, "-o", "yaml"}
	if ignoreNotFound {
		args = append(args, "--ignore-not-found")
	}
	if len(namespace) > 0 {
		args = append(args, "--namespace", namespace)
		location = filepath.Join(d.location, namespace)
	}
	location = filepath.Join(location, name+".yaml")
	output, err := d.runCmd(args...)
	if err != nil {
		d.logError(err.Error(), args...)
		log.Printf("Error: get resource %s in namespace %s: %v", name, namespace, err)
		return addToArchive(location, d.mode, []byte(err.Error()), tw)
	}

	if ignoreNotFound && len(output) == 0 {
		return nil
	}
	return addToArchive(location, d.mode, output, tw)
}

func (d *Dumper) logError(err string, args ...string) {
	d.errors += d.cmd + " " + strings.Join(args, " ") + "\n" + err + "\n\n"
}

func addToArchive(location string, mode int64, content []byte, tw *tar.Writer) error {
	hdr := &tar.Header{
		Name:    location,
		Mode:    mode,
		ModTime: time.Now(),
		Size:    int64(len(content)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return errors.Wrapf(err, "write header to %s", location)
	}
	if _, err := tw.Write(content); err != nil {
		return errors.Wrapf(err, "write content to %s", location)
	}

	return nil
}

type crSecrets struct {
	Spec struct {
		SecretName string `json:"secretsName,omitempty"`
		Secrets    struct {
			Users string `json:"users,omitempty"`
		} `json:"secrets,omitempty"`
		Users []struct {
			Name       string `json:"name,omitempty"`
			SecretName string `json:"secretName,omitempty"`
		} `json:"users,omitempty"`
	} `json:"spec"`
}

func (d *Dumper) getIndividualFiles(namespace string, podName, path, location string, tw *tar.Writer) error {
	if len(d.fileContainer) == 0 {
		return errors.Errorf("Logs container name is not specified for resource %s in namespace %s", resourceType(d.crType), d.namespace)
	}
	args := []string{"-n", namespace, "-c", d.fileContainer, "cp", podName + ":" + path, "/dev/stdout"}
	output, err := d.runCmd(args...)
	if err != nil {
		d.logError(err.Error(), args...)
		log.Printf("Error: get path %s for resource %s in namespace %s: %v", path, resourceType(d.crType), d.namespace, err)
		return addToArchive(location, d.mode, []byte(err.Error()), tw)
	}

	if len(output) == 0 {
		return nil
	}
	return addToArchive(location+"/"+path, d.mode, output, tw)
}

func (d *Dumper) getPodSummary(resource, podName, crName string, namespace string, tw *tar.Writer) ([]byte, error) {
	var (
		summCmdName string
		ports       string
		summCmdArgs []string
	)

	switch resource {
	case "ps":
		fallthrough
	case "pxc":
		var pass, port string
		if d.forwardport != "" {
			port = d.forwardport
		} else {
			port = "3306"
		}
		cr, err := d.getCR(resource+"/"+crName, namespace)
		if err != nil {
			return nil, errors.Wrap(err, "get cr")
		}
		if cr.Spec.SecretName != "" {
			pass, err = d.getDataFromSecret(cr.Spec.SecretName, "root", namespace)
		} else {
			pass, err = d.getDataFromSecret(crName+"-secrets", "root", namespace)
		}
		if err != nil {
			return nil, errors.Wrap(err, "get password from pxc users secret")
		}
		ports = port + ":3306"
		summCmdName = "pt-mysql-summary"
		summCmdArgs = []string{"--host=127.0.0.1", "--port=" + port, "--user=root", "--password=" + string(pass)}
	case "pg", "pgv2":
		var kubeconfig string
		if d.kubeconfig != "" {
			kubeconfig = " --kubeconfig=" + d.kubeconfig
		}
		summCmdName = "sh"
		summCmdArgs = []string{"-c", "curl https://raw.githubusercontent.com/percona/support-snippets/master/postgresql/pg_gather/gather.sql 2>/dev/null | " +
			d.cmd + kubeconfig + " -n " + namespace + " exec -i " + podName + " -- psql -X -f - "}
	case "psmdb":
		var port string
		if d.forwardport != "" {
			port = d.forwardport
		} else {
			port = "27017"
		}
		cr, err := d.getCR("psmdb/"+crName, namespace)
		if err != nil {
			return nil, errors.Wrap(err, "get cr")
		}
		user, err := d.getDataFromSecret(cr.Spec.Secrets.Users, "MONGODB_DATABASE_ADMIN_USER", namespace)
		if err != nil {
			return nil, errors.Wrap(err, "get user name from psmdb users secret")
		}
		pass, err := d.getDataFromSecret(cr.Spec.Secrets.Users, "MONGODB_DATABASE_ADMIN_PASSWORD", namespace)
		if err != nil {
			return nil, errors.Wrap(err, "get password from psmdb users secret")
		}
		ports = port + ":27017"
		summCmdName = "pt-mongodb-summary"
		summCmdArgs = []string{"--username=" + user, "--password=" + string(pass), "--authenticationDatabase=admin", "127.0.0.1:" + port}
	}

	cmdPortFwd := exec.Command(d.cmd, "port-forward", "pod/"+podName, ports, "-n", namespace, "--kubeconfig", d.kubeconfig)
	go func() {
		err := cmdPortFwd.Run()
		if err != nil {
			d.logError(err.Error(), "port-forward")
		}
	}()
	defer func() {
		err := cmdPortFwd.Process.Kill()
		if err != nil {
			d.logError(err.Error(), "kill port-forward")
		}
	}()

	time.Sleep(3 * time.Second) // wait for port-forward command

	var outb, errb bytes.Buffer
	cmd := exec.Command(summCmdName, summCmdArgs...)
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		return nil, errors.Errorf("error: %v\nstderr: %sstdout: %s", err, errb.String(), outb.String())
	}
	return outb.Bytes(), nil
}

func (d *Dumper) getCR(crName string, namespace string) (crSecrets, error) {
	var cr crSecrets
	output, err := d.runCmd("get", crName, "-o", "json", "-n", namespace)
	if err != nil {
		return cr, errors.Wrap(err, "get "+crName)
	}
	err = json.Unmarshal(output, &cr)
	if err != nil {
		return cr, errors.Wrap(err, "unmarshal "+crName+" cr")
	}

	return cr, nil
}

func (d *Dumper) getDataFromSecret(secretName, dataName string, namespace string) (string, error) {
	passEncoded, err := d.runCmd("get", "secrets/"+secretName, "--template={{.data."+dataName+"}}", "-n", namespace)
	if err != nil {
		return "", errors.Wrap(err, "run get secret cmd")
	}
	pass, err := base64.StdEncoding.DecodeString(string(passEncoded))
	if err != nil {
		return "", errors.Wrap(err, "decode data")
	}

	return string(pass), nil
}

func resourceType(s string) string {
	if s == "pxc" || strings.HasPrefix(s, "pxc/") {
		return "pxc"
	} else if s == "psmdb" || strings.HasPrefix(s, "psmdb/") {
		return "psmdb"
	} else if s == "pg" || strings.HasPrefix(s, "pg/") {
		return "pg"
	} else if s == "pgv2" || strings.HasPrefix(s, "pgv2/") {
		return "pgv2"
	} else if s == "ps" || strings.HasPrefix(s, "ps/") {
		return "ps"
	}
	return s
}
