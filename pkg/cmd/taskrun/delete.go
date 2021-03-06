// Copyright © 2019 The Tekton Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package taskrun

import (
	"errors"
	"fmt"

	"github.com/tektoncd/cli/pkg/formatted"
	taskpkg "github.com/tektoncd/cli/pkg/task"
	trsort "github.com/tektoncd/cli/pkg/taskrun/sort"
	"github.com/tektoncd/pipeline/pkg/apis/pipeline/v1beta1"

	"github.com/spf13/cobra"
	"github.com/tektoncd/cli/pkg/actions"
	"github.com/tektoncd/cli/pkg/cli"
	"github.com/tektoncd/cli/pkg/deleter"
	"github.com/tektoncd/cli/pkg/options"
	trlist "github.com/tektoncd/cli/pkg/taskrun/list"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	cliopts "k8s.io/cli-runtime/pkg/genericclioptions"
)

type deleteOptions struct {
	ClusterTaskName string
	TaskName        string
}

func deleteCommand(p cli.Params) *cobra.Command {
	opts := &options.DeleteOptions{Resource: "TaskRun", ForceDelete: false, DeleteAllNs: false}
	deleteOpts := &deleteOptions{}
	f := cliopts.NewPrintFlags("delete")
	eg := `Delete TaskRuns with names 'foo' and 'bar' in namespace 'quux':

    tkn taskrun delete foo bar -n quux

or

    tkn tr rm foo bar -n quux
`

	c := &cobra.Command{
		Use:               "delete",
		Aliases:           []string{"rm"},
		Short:             "Delete TaskRuns in a namespace",
		Example:           eg,
		ValidArgsFunction: formatted.ParentCompletion,
		Args:              cobra.MinimumNArgs(0),
		SilenceUsage:      true,
		Annotations: map[string]string{
			"commandType": "main",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s := &cli.Stream{
				In:  cmd.InOrStdin(),
				Out: cmd.OutOrStdout(),
				Err: cmd.OutOrStderr(),
			}

			if deleteOpts.TaskName != "" && deleteOpts.ClusterTaskName != "" {
				return fmt.Errorf("cannot use --task and --clustertask option together")
			}

			if deleteOpts.ClusterTaskName != "" {
				opts.ParentResource = "ClusterTask"
				opts.ParentResourceName = deleteOpts.ClusterTaskName
			} else {
				opts.ParentResource = "Task"
				opts.ParentResourceName = deleteOpts.TaskName
			}

			if opts.Keep < 0 {
				return fmt.Errorf("keep option should not be lower than 0")
			}

			if opts.Keep > 0 && opts.ParentResourceName == "" {
				opts.DeleteAllNs = true
			}

			if err := opts.CheckOptions(s, args, p.Namespace()); err != nil {
				return err
			}

			return deleteTaskRuns(s, p, args, opts)
		},
	}
	f.AddFlags(c)
	c.Flags().BoolVarP(&opts.ForceDelete, "force", "f", false, "Whether to force deletion (default: false)")
	c.Flags().StringVarP(&deleteOpts.TaskName, "task", "t", "", "The name of a Task whose TaskRuns should be deleted (does not delete the task)")
	c.Flags().StringVarP(&deleteOpts.ClusterTaskName, "clustertask", "", "", "The name of a ClusterTask whose TaskRuns should be deleted (does not delete the ClusterTask)")
	c.Flags().BoolVarP(&opts.DeleteAllNs, "all", "", false, "Delete all TaskRuns in a namespace (default: false)")
	c.Flags().IntVarP(&opts.Keep, "keep", "", 0, "Keep n most recent number of TaskRuns")
	return c
}

func deleteTaskRuns(s *cli.Stream, p cli.Params, trNames []string, opts *options.DeleteOptions) error {
	trGroupResource := schema.GroupVersionResource{Group: "tekton.dev", Resource: "taskruns"}
	cs, err := p.Clients()
	if err != nil {
		return fmt.Errorf("failed to create tekton client")
	}
	var d *deleter.Deleter
	switch {
	case opts.DeleteAllNs:
		d = deleter.New("TaskRun", func(taskRunName string) error {
			return actions.Delete(trGroupResource, cs, taskRunName, p.Namespace(), metav1.DeleteOptions{})
		})
		trs, err := allTaskRunNames(cs, opts.Keep, p.Namespace())
		if err != nil {
			return err
		}
		d.Delete(s, trs)
	case opts.ParentResourceName == "":
		d = deleter.New("TaskRun", func(taskRunName string) error {
			return actions.Delete(trGroupResource, cs, taskRunName, p.Namespace(), metav1.DeleteOptions{})
		})
		d.Delete(s, trNames)
	default:
		d = deleter.New(opts.ParentResource, func(_ string) error {
			err := fmt.Sprintf("the %s should not be deleted", opts.ParentResource)
			return errors.New(err)
		})
		d.WithRelated("TaskRun", taskRunLister(p, opts.Keep, opts.ParentResource, cs), func(taskRunName string) error {
			return actions.Delete(trGroupResource, cs, taskRunName, p.Namespace(), metav1.DeleteOptions{})
		})
		d.DeleteRelated(s, []string{opts.ParentResourceName})
	}

	if !opts.DeleteAllNs {
		if d.Errors() == nil {
			switch {
			case opts.Keep > 0:
				// Should only occur in case of --task flag and --keep being used together
				fmt.Fprintf(s.Out, "All but %d TaskRuns associated with %s %q deleted in namespace %q\n", opts.Keep, opts.ParentResource, opts.ParentResourceName, p.Namespace())
			case opts.ParentResourceName != "":
				fmt.Fprintf(s.Out, "All TaskRuns associated with %s %q deleted in namespace %q\n", opts.ParentResource, opts.ParentResourceName, p.Namespace())
			default:
				d.PrintSuccesses(s)
			}
		}
	} else if opts.DeleteAllNs {
		if d.Errors() == nil {
			if opts.Keep > 0 {
				fmt.Fprintf(s.Out, "All but %d TaskRuns deleted in namespace %q\n", opts.Keep, p.Namespace())
			} else {
				fmt.Fprintf(s.Out, "All TaskRuns deleted in namespace %q\n", p.Namespace())
			}
		}
	}
	return d.Errors()
}

func taskRunLister(p cli.Params, keep int, kind string, cs *cli.Clients) func(string) ([]string, error) {
	return func(taskName string) ([]string, error) {
		label := "task"
		if kind == "ClusterTask" {
			label = "clusterTask"
		}

		lOpts := metav1.ListOptions{
			LabelSelector: fmt.Sprintf("tekton.dev/%s=%s", label, taskName),
		}
		trs, err := trlist.TaskRuns(cs, lOpts, p.Namespace())
		if err != nil {
			return nil, err
		}
		if kind == "Task" {
			trs.Items = taskpkg.FilterByRef(trs.Items, string(v1beta1.NamespacedTaskKind))
		}
		return keepTaskRuns(trs, keep), nil
	}
}

func allTaskRunNames(cs *cli.Clients, keep int, ns string) ([]string, error) {
	taskRuns, err := trlist.TaskRuns(cs, metav1.ListOptions{}, ns)
	if err != nil {
		return nil, err
	}
	return keepTaskRuns(taskRuns, keep), nil
}

func keepTaskRuns(taskRuns *v1beta1.TaskRunList, keep int) []string {
	var names []string
	var counter = 0

	// Do not sort TaskRuns if keep=0 since ordering won't matter
	if keep > 0 {
		trsort.SortByStartTime(taskRuns.Items)
	}

	for _, tr := range taskRuns.Items {
		if keep > 0 && counter != keep {
			counter++
			continue
		}
		names = append(names, tr.Name)
	}
	return names
}
