import React, { PropTypes } from "react";

import Stats from "../components/Stats";
import Graph from "./Graph";
import Properties from "../components/Properties";

import { Button } from "react-bootstrap";

const ResponseInfo = () => (
    <div style={{ fontSize: "13px", flex: "0 auto" }}>
        <Properties currentNode={this.state.currentNode} />
        <div style={{ display: "flex" }}>
            <div style={{ flex: "0 0 50%" }}>
                <Stats
                    rendering={this.props.rendering}
                    latency={this.props.latency}
                />
                <div>
                    Nodes:{" "}
                    {this.props.allNodes.length}
                    , Edges:{" "}
                    {this.props.allEdges.length}
                </div>
            </div>
            <div style={{ flex: "0 0 50%" }}>
                <Button
                    className="Response-button"
                    bsStyle="primary"
                    disabled={
                        this.props.allNodes.length === 0 ||
                            this.isFullyExpanded(this.props)
                    }
                    onClick={() => this.refs.graph.expandAll()}
                >
                    {this.state.fullyExpanded ? "Collapse" : "Expand"}
                </Button>
                <Button
                    className="Response-button"
                    bsStyle="primary"
                    disabled={this.props.allNodes.length === 0}
                    onClick={() =>
                        this.props.renderGraph(
                            this.props.result,
                            !this.props.treeView,
                        )}
                >
                    {this.props.treeView ? "Graph view" : "Tree View"}
                </Button>
            </div>
        </div>
        <div style={{ height: "auto" }}>
            <i>
                {this.props.allNodes.length !== 0 && !this.state.fullyExpanded
                    ? "We have only loaded a subset of the graph. Double click on a leaf node to expand its child nodes."
                    : ""}
            </i>
        </div>
    </div>
);

// Editor.propTypes = {

// }

export default ResponseInfo;