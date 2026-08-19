const test = require('node:test');
const assert = require('node:assert/strict');
const { addNodeToGraph, layoutGraph } = require('./mind-map.js');

function graph() {
	return {
		root_id: 'root',
		nodes: [
			{ id: 'root', label: 'Estudo' },
			{ id: 'biology', parent_id: 'root', label: 'Biologia' },
			{ id: 'physics', parent_id: 'root', label: 'Física' },
		],
	};
}

test('novo nó é ligado ao pai explicitamente selecionado', () => {
	const value = graph();
	const node = addNodeToGraph(value, 'biology', 'photosynthesis');

	assert.equal(node.parent_id, 'biology');
	assert.equal(value.nodes.find((item) => item.id === 'photosynthesis').parent_id, 'biology');
});

test('não cria nó sem um pai existente', () => {
	assert.throws(() => addNodeToGraph(graph(), '', 'orphan'), /Selecione um ramo pai/);
	assert.throws(() => addNodeToGraph(graph(), 'missing', 'orphan'), /Selecione um ramo pai/);
});

test('layout preserva níveis e separa irmãos em uma árvore', () => {
	const value = graph();
	addNodeToGraph(value, 'biology', 'photosynthesis');
	const layout = layoutGraph(value);

	assert.ok(layout.positions.root.x < layout.positions.biology.x);
	assert.ok(layout.positions.biology.x < layout.positions.photosynthesis.x);
	assert.ok(layout.positions.biology.y < layout.positions.physics.y);
});
