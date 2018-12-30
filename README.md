# hostStats
collect host stats from multiple vcenter instances written in GO

Rename/edit the config.json.sample file and add your vcenters then execute the go script or alternatively build a executable first. Results will be saved to whatever path (including filename) set in config.


I made this in replacement to old PowerCLI scripts that wasn't very scalable and took very long time to execute. This uses golangs fantastic multithreading capability which reduced data collection time by 98.5% compared to PowerCLI scripts it replaced.
