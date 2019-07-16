# Gather Dependencies Tool

Build minimal containers for dynamically linked applications. This enables you to build Docker images from `scratch` for dynamically linked Go applications using `CGO_ENABLED`. Furthermore, it can be easily integrated into a fully automated CI/CD pipeline to build Docker images including all required libraries.

## Problem description

When building Docker images from `scratch` for Go applications with `CGO_ENABLED`, you will probably have trouble starting the container. Instead of your precious application output, you only read something like the following in the console:

```
standard_init_linux.go:211: exec user process caused "no such file or directory"
```

This happens when dynamically linked libraries are not available on your system, which is likely the case when building Docker images from `scratch`. This problem can be solved by manually copying all required libraries to the container, but that can be a tedious task.

The `gather-dependencies` tool presented here offers a convenient way of recursively collecting all required libraries for a dynamically linked binary file. What does *recursively* mean? The linked libraries might have other dependencies, that are not directly required by your application. These libraries (and their dependencies, and their dependencie's dependencies ...) are also acquired by this tool.

## Usage

Gather all dependencies of binary file `{BINARY}` and copy them to a directory called `lib`. The `--clean` flag will delete all existing files and directories in `lib`:

```
gather-dependencies {BINARY} lib --clean
```

All library files will be copied to lib, resembling the original file system hierarchy. Thus, the directory `lib` can be simply added to the root of a Docker container to correctly install the libraries:

```
FROM scratch

ADD lib /
```

Now you only need to add your application binary and resources to the container and set up an entry point.

That's it :-)