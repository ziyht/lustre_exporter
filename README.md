# Lustre Metrics Exporter

rebuild from HewlettPackard's [LustreExporter](https://github.com/HewlettPackard/lustre_exporter)

This project was created because the original project is no longer maintained, and to some extent, the performance of the original version is insufficient, sometimes causing the collecting process to time out.

In case of this, we focus on performance optimization when rebuiding this new project.

Of course, we also retain the processing logic of the old version, to ensure the correctness of the new version of the collection logic by comparing the data collected by the old one.

New Functions:
1. Add v2 vesion collect logic(default)  
    1. do some performance optimization, can execute 7-10x faster than old version for large files parsing  
       * using cache to skip read and parsing file repeatedly  
       * reduce regexp-based text processing 
    2. add limitation of runtimes(default 4), if runtimes reach limit, the new request will wait and get data from the prev last request

New Falgs:
* --collector.path.proc="/proc"
* --collector.path.sys="/sys"
* --collector.collect.ver="v2"  
  default is 'v2', it will change the interval collecting logic to old when != 'v2'
* --collector.maxWorker=4  
  max runtime can create in the same time for v2 version


## Getting

```
git clone https://github.com/ziyht/lustre_exporter.git
```

## Building

```
cd lustre_exporter
go mod tidy
make build
```

## Running

```
./lustre_exporter <flags>
```

### Flags

* collector.ost=disabled/core/extended
* collector.mdt=disabled/core/extended
* collector.mgs=disabled/core/extended
* collector.mds=disabled/core/extended
* collector.client=disabled/core/extended
* collector.generic=disabled/core/extended
* collector.lnet=disabled/core/extended
* collector.health=disabled/core/extended

All above flags default to the value "extended" when no argument is submitted by the user.

Example: `./lustre_exporter --collector.ost=disabled --collector.mdt=core --collector.mgs=extended`

The above example will result in a running instance of the Lustre Exporter with the following statuses:
* collector.ost=disabled
* collector.mdt=core
* collector.mgs=extended
* collector.mds=extended
* collector.client=extended
* collector.generic=extended
* collector.lnet=extended
* collector.health=extended

Flag Option Detailed Description

- disabled - Completely disable all metrics for this portion of a source.
- core - Enable this source, but only for metrics considered to be particularly useful.
- extended - Enable this source and include all metrics that the Lustre Exporter is aware of within it.

## What's exported?

All Lustre procfs and procsys data from all nodes running the Lustre Exporter that we perceive as valuable data is exported or can be added to be exported (we don't have any known major gaps that anyone cares about, so if you see something missing, please file an issue!).

See the issues tab for all known issues.

## Troubleshooting

In the event that you encounter issues with specific metrics (especially on versions of Lustre older than 2.7), please try disabling those specific troublesome metrics using the documented collector flags in the 'disabled' or 'core' state. Users have encountered bugs within Lustre where specific sysfs and procfs files miscommunicate their sizes, causing read calls to fail.

## Contributing

To contribute to this HPE project, you'll need to fill out a CLA (Contributor License Agreement). If you would like to contribute anything more than a bug fix (feature, architectural change, etc), please file an issue and we'll get in touch with you to have you fill out the CLA. 
