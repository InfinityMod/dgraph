import React from "react";

import Label from "../components/Label";

import "../assets/css/Graph.css";

const Graph = (
    { text, success, plotAxis }, // var graphClass = classNames( //     { Graph: true, "Graph-fs": this.props.graph === "fullscreen" }, //     { "Graph-s": this.props.graph !== "fullscreen" }, //     { "Graph-error": this.props.resType === "error-res" }, //     { "Graph-success": this.props.resType === "success-res" }, //     { "Graph-hourglass": this.props.resType === "hourglass" }, // );
) => (
    <div className="Graph-wrapper">
        <div className="Graph-fixed-height">
            <div id="graph" className="Graph Graph-s">
                {text}
            </div>
        </div>
        <div className="Graph-label-box">
            <div className="Graph-label">
                {plotAxis.map(
                    function(label, i) {
                        return (
                            <Label
                                key={i}
                                color={label.color}
                                pred={label.pred}
                                label={label.label}
                            />
                        );
                    },
                    this,
                )}
            </div>
        </div>
    </div>
);

export default Graph;
