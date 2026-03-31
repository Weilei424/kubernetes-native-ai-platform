---
name: architecture-diagram-creator
description: Create comprehensive HTML architecture diagrams showing data flows, business objectives, features, technical architecture, and deployment. Use when users request system architecture, project documentation, high-level overviews, or technical specifications.
---

# Architecture Diagram Creator

Create comprehensive HTML architecture diagrams that explain a project from both business and technical perspectives. Use this skill when users ask for system architecture, high-level overviews, technical documentation, data flow diagrams, processing pipelines, or deployment views.

## When to Use

Use this skill when the user asks to:

- Create an architecture diagram for a project
- Generate a high-level system overview
- Document system architecture
- Show data flow and processing pipelines
- Explain business context together with technical implementation
- Produce architecture documentation in HTML format

## Goal

Generate a single self-contained HTML file that presents:

1. Business context
2. Data flow diagram
3. Processing pipeline
4. System architecture layers
5. Feature summary
6. Deployment and workflow information

The output should be visually clear, technically accurate, and based on real project details extracted from the available materials.

## Inputs

The skill may use:

- README files
- Project source code and folder structure
- Existing technical docs
- User prompts describing the system
- Configuration files such as Docker, Kubernetes, Terraform, CI/CD, or deployment manifests

## Output

Write a file named:

`[project-name]-architecture.html`

The HTML must be:

- Self-contained
- Readable in a browser without external dependencies
- Structured with clear sections
- Styled with consistent spacing, typography, and semantic colors
- Based on actual project details rather than generic placeholders whenever possible

## Required Sections

### 1. Business Context

Include:

- Project purpose
- Target users
- Core business value
- Main objectives
- Success metrics or expected outcomes

### 2. Data Flow Diagram

Create an SVG diagram showing:

- Data sources
- Inputs
- Processing components
- Storage or intermediate systems
- Outputs and consumers

Use left-to-right flow where possible.

### 3. Processing Pipeline

Show the system as a staged pipeline, such as:

- Input / ingestion
- Validation / transformation
- Core processing / business logic
- Persistence / messaging
- Delivery / output

### 4. System Architecture

Present the architecture in layers, such as:

- Client / user layer
- API / gateway layer
- Service / application layer
- Data / storage layer
- Infrastructure / platform layer

### 5. Features

Include both:

- Functional requirements
- Non-functional requirements

Examples:

- Authentication
- Real-time processing
- Scalability
- Reliability
- Observability
- Security
- Maintainability

### 6. Deployment

Include:

- Deployment model
- Environments
- Dependencies and prerequisites
- Runtime components
- Developer or deployment workflow
- Optional CI/CD notes if relevant

## Workflow

1. Analyze the project materials
2. Extract the real system purpose, users, and value
3. Identify data sources, processing stages, services, and outputs
4. Infer the system layers from the codebase or docs
5. Summarize major features and technical requirements
6. Determine how the system is deployed or intended to be deployed
7. Generate a polished HTML file with all required sections
8. Ensure the diagrams and labels use project-specific terminology

## Diagram Design Rules

### Visual Structure

- Keep diagrams clean and uncluttered
- Use consistent spacing and alignment
- Use clear labels
- Prefer horizontal left-to-right data flow
- Group related components visually

### Semantic Colors

Use these semantic colors consistently:

- Data: `#4299e1`
- Processing: `#ed8936`
- AI / intelligence / orchestration: `#9f7aea`
- Success / output: `#48bb78`
- Neutral borders / arrows / text accents: `#666666`

## HTML Template

```html
<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>[Project] Architecture</title>
    <style>
        body {
            font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
            max-width: 1200px;
            margin: 0 auto;
            padding: 20px;
            line-height: 1.6;
            color: #1a202c;
            background: #f7fafc;
        }

        h1 {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 30px;
            border-radius: 16px;
            margin-bottom: 30px;
        }

        h2 {
            margin-top: 0;
            color: #2d3748;
        }

        .section {
            background: white;
            border-radius: 16px;
            padding: 24px;
            margin: 30px 0;
            box-shadow: 0 4px 16px rgba(0,0,0,0.08);
        }

        .grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
            gap: 16px;
        }

        .card {
            border-radius: 12px;
            padding: 16px;
            background: #edf2f7;
        }

        .tag {
            display: inline-block;
            padding: 4px 10px;
            border-radius: 999px;
            font-size: 12px;
            font-weight: 600;
            margin: 4px 6px 0 0;
        }

        .data { background: #bee3f8; color: #1a365d; }
        .processing { background: #fbd38d; color: #7b341e; }
        .ai { background: #d6bcfa; color: #44337a; }
        .success { background: #c6f6d5; color: #22543d; }

        svg {
            width: 100%;
            height: auto;
            max-width: 100%;
        }

        ul {
            margin: 0;
            padding-left: 20px;
        }
    </style>
</head>
<body>
<h1>[Project Name] - Architecture Overview</h1>

<div class="section">
    <h2>Business Context</h2>
    <!-- objectives, users, value, metrics -->
</div>

<div class="section">
    <h2>Data Flow</h2>
    <!-- SVG data flow diagram -->
</div>

<div class="section">
    <h2>Processing Pipeline</h2>
    <!-- SVG or stage cards -->
</div>

<div class="section">
    <h2>System Architecture</h2>
    <!-- layered architecture -->
</div>

<div class="section">
    <h2>Features</h2>
    <!-- functional + non-functional -->
</div>

<div class="section">
    <h2>Deployment</h2>
    <!-- deployment model, prerequisites, workflows -->
</div>
</body>
</html>
```

## SVG Pattern for Data Flow

```html
<svg viewBox="0 0 800 400" xmlns="http://www.w3.org/2000/svg">
    <defs>
        <marker id="arrow" markerWidth="10" markerHeight="10" refX="9" refY="3" orient="auto" markerUnits="strokeWidth">
            <path d="M0,0 L0,6 L9,3 z" fill="#666" />
        </marker>
    </defs>

    <rect x="50" y="150" width="120" height="80" rx="12" fill="#4299e1"/>
    <text x="110" y="195" text-anchor="middle" fill="white" font-size="14" font-weight="600">Sources</text>

    <rect x="340" y="150" width="120" height="80" rx="12" fill="#ed8936"/>
    <text x="400" y="195" text-anchor="middle" fill="white" font-size="14" font-weight="600">Processing</text>

    <rect x="630" y="150" width="120" height="80" rx="12" fill="#48bb78"/>
    <text x="690" y="195" text-anchor="middle" fill="white" font-size="14" font-weight="600">Outputs</text>

    <path d="M170,190 L340,190" stroke="#666" stroke-width="2.5" fill="none" marker-end="url(#arrow)"/>
    <path d="M460,190 L630,190" stroke="#666" stroke-width="2.5" fill="none" marker-end="url(#arrow)"/>
</svg>
```

## Content Rules

- Use real project names, components, and technologies when available
- Do not leave generic placeholders if project details can be inferred
- Keep explanations concise but meaningful
- Balance business explanation with technical depth
- Prefer clarity over decorative complexity
- Make the file useful for both technical and non-technical readers
- Show relationships between components, not just a list of tools

## Extraction Guidance

When analyzing a project, extract:

### Business Context

- Why the system exists
- Who uses it
- What problem it solves
- What value it creates

### Data Flow

- What enters the system
- How data moves
- What transformations occur
- What outputs are produced

### Architecture

- Frontend or client components
- APIs and services
- Databases, caches, queues, models
- Infra and deployment platforms

### Deployment

- Local development setup
- Containers and orchestration
- CI/CD workflow
- Cloud or on-prem dependencies

## Example Triggers

- Create architecture diagram for this project
- Generate a high-level overview of the system
- Document the technical architecture
- Show me the data flow and deployment design
- Build an HTML architecture page from this repository

## Success Criteria

A successful result should:

- Produce a complete HTML architecture page
- Include all 6 required sections
- Reflect the actual project structure and purpose
- Clearly visualize data flow and system layers
- Be polished enough to use as project documentation
- Be understandable without reading the full codebase
