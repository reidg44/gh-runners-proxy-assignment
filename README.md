# gh-runners-proxy-assignment

## Problem Statement

A user creates a matrix GitHub Actions workflow with multiple jobs. Each job uses the same self-hosted runner label, so GitHub randomly assigns available runners to jobs. The user wants to control which runner executes each job, but GitHub's API does not provide a way to specify this. For example, if a user has 5 runners with different hardware specs, they want to ensure that jobs requiring high CPU resources are assigned to the appropriate runners, rather than relying on random assignment.

## Proposed Solution

<https://github.com/actions/scaleset> is an open source Go SDK for Github Actions. It allows users to listen for jobs, generate just-in-time configurations.

We want to begin by creating a listener server using this SDK that can receive job events from GitHub. When a job is triggered, this server will spin up new runners to match the expected configurations for the job.

Next, we will implement a proxy server that can route job requests to the appropriate runners based on their configurations. This proxy will act as an intermediary between GitHub and the self-hosted runners, allowing us to control which runner executes each job.

For example, if 2 jobs are triggered in parallel, but one requires high CPU resources and the other does not, the proxy server can route the high CPU job to a runner with more powerful hardware, while routing the other job to a less powerful runner. It will do so by manipulating traffic to the self-hosted runners, ensuring that each job is executed on the appropriate runner based on its requirements.

## Tests

The workflow `.github/workflows/test-case.yaml` runs 8 jobs in parallel, each with the same label. One of the jobs is labeled high-cpu and the others are labeled low-cpu. The listener server will spin up 8 runners, 1 with high CPU resources and 7 with low CPU resources. The proxy server will route the high-cpu job to the runner with high CPU resources, while routing the low-cpu jobs to the runners with low CPU resources. We can verify that the jobs are executed on the correct runners by checking the logs and runner assignments in GitHub Actions.

Once we can reliabily route the jobs to the correct runners on multiple runs, we can assume we have successfully implemented the solution.
